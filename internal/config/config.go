package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

type Config struct {
	BaseDomain       string
	EnrollmentSecret string
	InitialCIDRs     []string
	TrustedProxies   []string
	AdminSocket      string
	EnrollRateLimit  int
	LogLevel         string
	ListenAddr       string
	AdminPageAddr    string
}

func Load() (*Config, error) {
	c := &Config{
		AdminSocket:     getEnv("CADDYGATE_ADMIN_SOCKET", "/run/caddy/admin.sock"),
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

	return c, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
