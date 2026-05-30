package enrollment

import (
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/yourorg/caddygate/internal/caddy"
)

const enrollPrefix = "/hello-its-me/"

// rateBucket tracks attempts per IP for rate limiting.
type rateBucket struct {
	count       int
	windowStart time.Time
}

// Handler handles the /hello-its-me/{secret} enrollment endpoint.
type Handler struct {
	secret         string
	caddy          *caddy.Client
	rateLimit      int
	trustedProxies []string
	log            *slog.Logger

	mu      sync.Mutex
	buckets map[string]*rateBucket
}

func NewHandler(secret string, caddy *caddy.Client, rateLimit int, trustedProxies []string, log *slog.Logger) *Handler {
	h := &Handler{
		secret:         secret,
		caddy:          caddy,
		rateLimit:      rateLimit,
		trustedProxies: trustedProxies,
		log:            log,
		buckets:        make(map[string]*rateBucket),
	}
	go h.cleanBuckets()
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, enrollPrefix) {
		http.NotFound(w, r)
		return
	}

	ip := h.extractIP(r)

	// Rate limit BEFORE checking secret — prevents enumeration timing attacks
	if !h.checkRateLimit(ip) {
		h.log.Warn("enrollment rate limit exceeded", "ip", ip)
		http.NotFound(w, r)
		return
	}

	// Must be exactly /hello-its-me/<secret> with no trailing segments
	segment := strings.TrimPrefix(r.URL.Path, enrollPrefix)
	segment = strings.TrimSuffix(segment, "/")

	// Any failure: wrong secret, missing, extra segments — all return 404
	if segment == "" || strings.Contains(segment, "/") || segment != h.secret {
		http.NotFound(w, r)
		return
	}

	// Valid secret — enroll the IP
	parsed := net.ParseIP(ip)
	prefix := "/32"
	if parsed != nil && parsed.To4() == nil {
		prefix = "/128"
	}
	cidr := ip + prefix

	already, err := h.caddy.IsAllowed(ip)
	if err != nil {
		h.log.Error("checking allowlist", "err", err)
		http.NotFound(w, r)
		return
	}

	if already {
		h.log.Info("enrollment: already enrolled", "ip", ip)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(alreadyPage))
		return
	}

	if err := h.caddy.AddAllowedRange(cidr); err != nil {
		h.log.Error("adding IP to allowlist", "ip", ip, "err", err)
		http.NotFound(w, r)
		return
	}

	h.log.Info("enrollment: IP enrolled", "ip", ip)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(successPage))
}

func (h *Handler) extractIP(r *http.Request) string {
	remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)

	// 127.0.0.1 is always trusted — the sidecar always runs behind Caddy on localhost.
	trusted := remoteIP == "127.0.0.1"
	if !trusted {
		for _, cidr := range h.trustedProxies {
			if cidrContains(cidr, remoteIP) {
				trusted = true
				break
			}
		}
	}

	if trusted {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.Split(xff, ",")
			return strings.TrimSpace(parts[0])
		}
	}
	return remoteIP
}

func (h *Handler) checkRateLimit(ip string) bool {
	h.mu.Lock()
	defer h.mu.Unlock()

	now := time.Now()
	b, ok := h.buckets[ip]
	if !ok || now.Sub(b.windowStart) > time.Minute {
		h.buckets[ip] = &rateBucket{count: 1, windowStart: now}
		return true
	}
	b.count++
	return b.count <= h.rateLimit
}

func (h *Handler) cleanBuckets() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		h.mu.Lock()
		cutoff := time.Now().Add(-2 * time.Minute)
		for ip, b := range h.buckets {
			if b.windowStart.Before(cutoff) {
				delete(h.buckets, ip)
			}
		}
		h.mu.Unlock()
	}
}

func cidrContains(cidr, ip string) bool {
	_, network, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	return network.Contains(parsed)
}

const successPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Access granted</title>
<style>
  body { font-family: system-ui, sans-serif; max-width: 480px; margin: 80px auto; padding: 0 24px; color: #1a1a1a; }
  h1 { font-size: 1.4rem; font-weight: 500; margin-bottom: 0.5rem; }
  p  { color: #555; line-height: 1.6; }
</style>
</head>
<body>
  <h1>You're in.</h1>
  <p>Your IP has been added to the access list. You can now reach the services.</p>
</body>
</html>`

const alreadyPage = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Already enrolled</title>
<style>
  body { font-family: system-ui, sans-serif; max-width: 480px; margin: 80px auto; padding: 0 24px; color: #1a1a1a; }
  h1 { font-size: 1.4rem; font-weight: 500; margin-bottom: 0.5rem; }
  p  { color: #555; line-height: 1.6; }
</style>
</head>
<body>
  <h1>Already enrolled.</h1>
  <p>Your IP is already on the access list.</p>
</body>
</html>`
