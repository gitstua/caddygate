package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gitstua/caddygate/internal/admin"
	"github.com/gitstua/caddygate/internal/allowlist"
	"github.com/gitstua/caddygate/internal/caddy"
	"github.com/gitstua/caddygate/internal/config"
	"github.com/gitstua/caddygate/internal/ddns"
	"github.com/gitstua/caddygate/internal/discovery"
	"github.com/gitstua/caddygate/internal/enrollment"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))

	cfg, err := config.Load()
	if err != nil {
		log.Error("config error", "err", err)
		os.Exit(1)
	}

	// Adjust log level
	var level slog.Level
	switch cfg.LogLevel {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	log = slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))

	log.Info("caddygate starting",
		"base_domain", cfg.BaseDomain,
		"listen", cfg.ListenAddr,
	)

	// Wait for Caddy Admin API to become ready
	caddyClient := caddy.NewClient(cfg.AdminSocket)
	waitForCaddy(caddyClient, log)

	// Restore persisted allowlist first, then layer INITIAL_CIDRS on top.
	allowlist.Load(caddyClient, cfg.AllowlistFile, log)
	if len(cfg.InitialCIDRs) > 0 {
		if err := allowlist.Seed(caddyClient, cfg.InitialCIDRs, log); err != nil {
			log.Warn("allowlist seed partial failure", "err", err)
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Enforce wildcard TLS policy and trigger immediate cert fetch.
	if err := caddyClient.EnforceTLSPolicy(cfg.BaseDomain, cfg.DNSProvider, cfg.DNSAPIToken); err != nil {
		log.Warn("could not enforce TLS policy", "err", err)
	} else {
		log.Info("wildcard TLS policy enforced", "cert", "*."+cfg.BaseDomain)
	}
	if err := caddyClient.ProvisionCert("*." + cfg.BaseDomain); err != nil {
		log.Warn("could not provision wildcard cert", "err", err)
	}

	// Reconcile static services: remove IP-upstream routes that are no longer in config.
	if existing, err := caddyClient.GetManagedServices(); err == nil {
		wanted := make(map[string]bool, len(cfg.StaticServices))
		for _, svc := range cfg.StaticServices {
			wanted[svc.Name+"."+cfg.BaseDomain] = true
		}
		for _, svc := range existing {
			if caddy.IsIPUpstream(svc.Upstream) && !wanted[svc.Host] {
				if err := caddyClient.RemoveRoute(svc.Host); err != nil {
					log.Warn("failed to remove stale static route", "host", svc.Host, "err", err)
				} else {
					log.Info("removed stale static service", "host", svc.Host)
				}
			}
		}
	}

	// Seed static services defined via CADDYGATE_STATIC_SERVICES
	for _, svc := range cfg.StaticServices {
		host := svc.Name + "." + cfg.BaseDomain
		if err := caddyClient.UpsertRoute(host, svc.Upstream, ""); err != nil {
			log.Warn("static service seed failed", "host", host, "err", err)
		} else {
			log.Info("registered static service", "host", host, "upstream", svc.Upstream)
		}
	}

	// Persist allowlist to disk every minute so it survives restarts.
	saveFn := func() { allowlist.Save(caddyClient, cfg.AllowlistFile, log) }
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				saveFn() // final save on shutdown
				return
			case <-ticker.C:
				saveFn()
			}
		}
	}()

	// Start DDNS updater if enabled
	if cfg.DDNSEnabled {
		ddnsUpdater := ddns.New(cfg.BaseDomain, cfg.DNSAPIToken, cfg.DDNSInterval, log)
		go func() {
			if err := ddnsUpdater.Run(ctx); err != nil && err != context.Canceled {
				log.Error("ddns updater exited", "err", err)
			}
		}()
	}

	// Start Docker discovery agent
	agent := discovery.NewAgent(cfg.BaseDomain, caddyClient, log)
	go func() {
		if err := agent.Run(ctx); err != nil && err != context.Canceled {
			log.Error("discovery agent exited", "err", err)
		}
	}()

	// Enrollment HTTP server
	enrollHandler := enrollment.NewHandler(
		cfg.EnrollmentSecret,
		caddyClient,
		cfg.EnrollRateLimit,
		cfg.TrustedProxies,
		saveFn,
		log,
	)

	// Admin page server (LAN-only, not routed through Caddy)
	adminHandler := admin.NewHandler(cfg.BaseDomain, cfg.EnrollmentSecret, caddyClient, saveFn, log)
	adminSrv := &http.Server{
		Addr:         cfg.AdminPageAddr,
		Handler:      adminHandler,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}
	go func() {
		log.Info("admin page listening", "addr", cfg.AdminPageAddr)
		if err := adminSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("admin server error", "err", err)
		}
	}()

	mux := http.NewServeMux()
	mux.Handle("/hello-its-me/", enrollHandler)
	// Caddy on-demand TLS permission check — only allow *.BASE_DOMAIN
	mux.HandleFunc("/tls-check", func(w http.ResponseWriter, r *http.Request) {
		domain := r.URL.Query().Get("domain")
		if strings.HasSuffix(domain, "."+cfg.BaseDomain) {
			w.WriteHeader(http.StatusOK)
		} else {
			w.WriteHeader(http.StatusForbidden)
		}
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	})

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Info("enrollment server listening", "addr", cfg.ListenAddr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("enrollment server error", "err", err)
			cancel()
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutdownCancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error("server shutdown error", "err", err)
	}
	if err := adminSrv.Shutdown(shutdownCtx); err != nil {
		log.Error("admin server shutdown error", "err", err)
	}
}

// waitForCaddy polls the Caddy Admin API until it responds, with backoff.
func waitForCaddy(client *caddy.Client, log *slog.Logger) {
	for i := 0; i < 30; i++ {
		_, err := client.GetAllowedRanges()
		if err == nil {
			log.Info("caddy admin API ready")
			return
		}
		log.Debug("waiting for caddy admin API", "attempt", i+1, "err", err)
		time.Sleep(time.Duration(i+1) * time.Second)
	}
	log.Warn("caddy admin API not responding after 30 attempts, continuing anyway")
}
