package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/yourorg/caddygate/internal/allowlist"
	"github.com/yourorg/caddygate/internal/caddy"
	"github.com/yourorg/caddygate/internal/config"
	"github.com/yourorg/caddygate/internal/discovery"
	"github.com/yourorg/caddygate/internal/enrollment"
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

	// Seed initial CIDRs into Caddy allowlist
	if len(cfg.InitialCIDRs) > 0 {
		if err := allowlist.Seed(caddyClient, cfg.InitialCIDRs, log); err != nil {
			log.Warn("allowlist seed partial failure", "err", err)
		}
	}

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Start Docker discovery agent
	agent := discovery.NewAgent(cfg.BaseDomain, caddyClient, log)
	go func() {
		if err := agent.Run(ctx); err != nil && err != context.Canceled {
			log.Error("discovery agent exited", "err", err)
		}
	}()

	// Enrollment HTTP server
	enrollHandler := enrollment.NewHandler(
		cfg.EnrollmentUUID,
		caddyClient,
		cfg.EnrollRateLimit,
		cfg.TrustedProxies,
		log,
	)

	mux := http.NewServeMux()
	mux.Handle("/hello-its-me/", enrollHandler)
	// Health check for the sidecar itself
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	// Catch-all: 404
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
