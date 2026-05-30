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
	count    int
	windowStart time.Time
}

// Handler handles the /hello-its-me/{uuid} enrollment endpoint.
type Handler struct {
	uuid           string
	caddy          *caddy.Client
	rateLimit      int
	trustedProxies []string
	log            *slog.Logger

	mu      sync.Mutex
	buckets map[string]*rateBucket
}

func NewHandler(uuid string, caddy *caddy.Client, rateLimit int, trustedProxies []string, log *slog.Logger) *Handler {
	h := &Handler{
		uuid:           uuid,
		caddy:          caddy,
		rateLimit:      rateLimit,
		trustedProxies: trustedProxies,
		log:            log,
		buckets:        make(map[string]*rateBucket),
	}
	// Periodically clean up old rate buckets
	go h.cleanBuckets()
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	// Only handle /hello-its-me/* — anything else is not our concern
	if !strings.HasPrefix(r.URL.Path, enrollPrefix) {
		http.NotFound(w, r)
		return
	}

	ip := h.extractIP(r)

	// Rate limit BEFORE checking UUID — prevents enumeration timing attacks
	if !h.checkRateLimit(ip) {
		h.log.Warn("enrollment rate limit exceeded", "ip", ip)
		// Return 404 even on rate limit — reveal nothing
		http.NotFound(w, r)
		return
	}

	// Extract UUID from path — must be exactly /hello-its-me/<uuid> with no trailing segments
	segment := strings.TrimPrefix(r.URL.Path, enrollPrefix)
	segment = strings.TrimSuffix(segment, "/")

	// Any failure: wrong UUID, missing UUID, extra path segments — all return 404
	if segment == "" || strings.Contains(segment, "/") || segment != h.uuid {
		http.NotFound(w, r)
		return
	}

	// Valid UUID — enroll the IP
	cidr := ip + "/32"

	already, err := h.caddy.IsAllowed(ip)
	if err != nil {
		h.log.Error("checking allowlist", "err", err)
		http.NotFound(w, r) // still 404 — don't leak internal errors
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
	if len(h.trustedProxies) > 0 {
		remoteIP, _, _ := net.SplitHostPort(r.RemoteAddr)
		for _, cidr := range h.trustedProxies {
			if cidrContains(cidr, remoteIP) {
				if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
					// Take the leftmost (client) IP
					parts := strings.Split(xff, ",")
					return strings.TrimSpace(parts[0])
				}
			}
		}
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
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
