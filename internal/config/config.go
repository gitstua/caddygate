package config

import (
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type StaticService struct {
	Name     string // subdomain
	Upstream string // host:port dial address
}

type Config struct {
	BaseDomain       string
	EnrollmentSecret string
	InitialCIDRs     []string
	StaticServices   []StaticService
	TrustedProxies   []string
	AdminSocket      string
	AllowlistFile    string
	EnrollRateLimit  int
	LogLevel         string
	ListenAddr       string
	AdminPageAddr    string
	DNSProvider      string
	DNSAPIToken      string
	DDNSEnabled      bool
	DDNSInterval     time.Duration
}

func Load() (*Config, error) {
	c := &Config{
		AdminSocket:     getEnv("CADDYGATE_ADMIN_SOCKET", "/run/caddy/admin.sock"),
		AllowlistFile:   getEnv("CADDYGATE_ALLOWLIST_FILE", "/data/caddygate-allowlist.json"),
		EnrollRateLimit: 10,
		LogLevel:        getEnv("CADDYGATE_LOG_LEVEL", "info"),
		ListenAddr:      getEnv("CADDYGATE_LISTEN_ADDR", ":8081"),
		AdminPageAddr:   getEnv("CADDYGATE_ADMIN_ADDR", ":7080"),
	}

	c.BaseDomain = os.Getenv("CADDYGATE_BASE_DOMAIN")
	if c.BaseDomain == "" {
		return nil, fmt.Errorf("CADDYGATE_BASE_DOMAIN is required")
	}

	c.EnrollmentSecret = os.Getenv("CADDYGATE_ENROLLMENT_SECRET")
	if c.EnrollmentSecret == "" {
		c.EnrollmentSecret = os.Getenv("CADDYGATE_ENROLLMENT_UUID") // legacy name
	}
	if c.EnrollmentSecret == "" {
		return nil, fmt.Errorf("CADDYGATE_ENROLLMENT_SECRET is required")
	}

	if raw := os.Getenv("CADDYGATE_INITIAL_CIDRS"); raw != "" {
		for _, cidr := range strings.Split(raw, ",") {
			cidr = strings.TrimSpace(cidr)
			if cidr != "" {
				c.InitialCIDRs = append(c.InitialCIDRs, cidr)
			}
		}
	}

	if raw := os.Getenv("CADDYGATE_STATIC_SERVICES"); raw != "" {
		for _, entry := range strings.Split(raw, ",") {
			entry = strings.TrimSpace(entry)
			if entry == "" {
				continue
			}
			name, upstream, ok := strings.Cut(entry, "=")
			if !ok || strings.TrimSpace(name) == "" || strings.TrimSpace(upstream) == "" {
				return nil, fmt.Errorf("CADDYGATE_STATIC_SERVICES: invalid entry %q, expected name=upstream", entry)
			}
			dial, err := parseDialAddress(strings.TrimSpace(upstream))
			if err != nil {
				return nil, fmt.Errorf("CADDYGATE_STATIC_SERVICES: invalid upstream in %q: %w", entry, err)
			}
			c.StaticServices = append(c.StaticServices, StaticService{
				Name:     strings.TrimSpace(name),
				Upstream: dial,
			})
		}
	}

	if raw := os.Getenv("CADDYGATE_TRUSTED_PROXIES"); raw != "" {
		for _, cidr := range strings.Split(raw, " ") {
			cidr = strings.TrimSpace(cidr)
			if cidr != "" {
				c.TrustedProxies = append(c.TrustedProxies, cidr)
			}
		}
	}

	if raw := os.Getenv("CADDYGATE_ENROLL_RATE_LIMIT"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("CADDYGATE_ENROLL_RATE_LIMIT must be an integer: %w", err)
		}
		c.EnrollRateLimit = n
	}

	c.DNSProvider = getEnv("CADDYGATE_DNS_PROVIDER", "cloudflare")
	c.DNSAPIToken = os.Getenv("CADDYGATE_DNS_API_TOKEN")

	if os.Getenv("CADDYGATE_DDNS_ENABLED") == "true" {
		if c.DNSAPIToken == "" {
			return nil, fmt.Errorf("CADDYGATE_DDNS_ENABLED requires CADDYGATE_DNS_API_TOKEN")
		}
		c.DDNSEnabled = true
		c.DDNSInterval = 5 * time.Minute
		if raw := os.Getenv("CADDYGATE_DDNS_INTERVAL"); raw != "" {
			d, err := time.ParseDuration(raw)
			if err != nil {
				return nil, fmt.Errorf("CADDYGATE_DDNS_INTERVAL invalid duration: %w", err)
			}
			c.DDNSInterval = d
		}
	}

	return c, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// parseDialAddress converts a URL like http://host:port to a bare host:port dial string.
func parseDialAddress(raw string) (string, error) {
	if !strings.Contains(raw, "://") {
		return raw, nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	if u.Host == "" {
		return "", fmt.Errorf("could not extract host from %q", raw)
	}
	return u.Host, nil
}
