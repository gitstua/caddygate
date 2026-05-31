package ddns

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// Updater keeps the wildcard DNS A record for *.baseDomain pointed at the
// current public IP using the Cloudflare API.
type Updater struct {
	baseDomain string
	apiToken   string
	interval   time.Duration
	log        *slog.Logger
	http       *http.Client
	lastIP     string
}

func New(baseDomain, apiToken string, interval time.Duration, log *slog.Logger) *Updater {
	return &Updater{
		baseDomain: baseDomain,
		apiToken:   apiToken,
		interval:   interval,
		log:        log,
		http:       &http.Client{Timeout: 10 * time.Second},
	}
}

// Run performs an immediate update then repeats on interval until ctx is cancelled.
func (u *Updater) Run(ctx context.Context) error {
	if err := u.update(ctx); err != nil {
		u.log.Warn("ddns: initial update failed", "err", err)
	}

	ticker := time.NewTicker(u.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := u.update(ctx); err != nil {
				u.log.Warn("ddns: update failed", "err", err)
			}
		}
	}
}

func (u *Updater) update(ctx context.Context) error {
	ip, err := u.publicIP(ctx)
	if err != nil {
		return fmt.Errorf("get public IP: %w", err)
	}
	if ip == u.lastIP {
		u.log.Debug("ddns: IP unchanged", "ip", ip)
		return nil
	}

	zoneID, err := u.resolveZoneID(ctx)
	if err != nil {
		return fmt.Errorf("resolve zone: %w", err)
	}

	recordName := "*." + u.baseDomain
	if err := u.upsertRecord(ctx, zoneID, recordName, ip); err != nil {
		return fmt.Errorf("upsert record: %w", err)
	}

	u.log.Info("ddns: updated", "record", recordName, "ip", ip)
	u.lastIP = ip
	return nil
}

// publicIP fetches the current public IPv4 address, trying multiple services.
func (u *Updater) publicIP(ctx context.Context) (string, error) {
	endpoints := []string{
		"https://api.ipify.org",
		"https://icanhazip.com",
		"https://ifconfig.me/ip",
	}
	for _, ep := range endpoints {
		req, err := http.NewRequestWithContext(ctx, "GET", ep, nil)
		if err != nil {
			continue
		}
		resp, err := u.http.Do(req)
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 64))
		resp.Body.Close()
		if err != nil || resp.StatusCode != 200 {
			continue
		}
		ip := strings.TrimSpace(string(b))
		if ip != "" {
			return ip, nil
		}
	}
	return "", fmt.Errorf("all public IP endpoints failed")
}

// resolveZoneID finds the Cloudflare zone ID whose name is a suffix of baseDomain.
func (u *Updater) resolveZoneID(ctx context.Context) (string, error) {
	// Try progressively shorter suffixes: e.g. apps.example.com → example.com → com
	parts := strings.Split(u.baseDomain, ".")
	for i := 0; i < len(parts)-1; i++ {
		candidate := strings.Join(parts[i:], ".")
		id, err := u.lookupZone(ctx, candidate)
		if err == nil {
			return id, nil
		}
	}
	return "", fmt.Errorf("no Cloudflare zone found for %q", u.baseDomain)
}

func (u *Updater) lookupZone(ctx context.Context, name string) (string, error) {
	var result struct {
		Result []struct {
			ID string `json:"id"`
		} `json:"result"`
		Success bool `json:"success"`
	}
	if err := u.cfGet(ctx, "https://api.cloudflare.com/client/v4/zones?name="+name+"&status=active", &result); err != nil {
		return "", err
	}
	if !result.Success || len(result.Result) == 0 {
		return "", fmt.Errorf("zone %q not found", name)
	}
	return result.Result[0].ID, nil
}

func (u *Updater) upsertRecord(ctx context.Context, zoneID, name, ip string) error {
	// Look for an existing A record.
	var list struct {
		Result []struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		} `json:"result"`
		Success bool `json:"success"`
	}
	url := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records?type=A&name=%s", zoneID, name)
	if err := u.cfGet(ctx, url, &list); err != nil {
		return err
	}

	body := fmt.Sprintf(`{"type":"A","name":%q,"content":%q,"ttl":60,"proxied":false}`, name, ip)

	if len(list.Result) > 0 {
		if list.Result[0].Content == ip {
			return nil // already correct
		}
		putURL := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records/%s", zoneID, list.Result[0].ID)
		return u.cfWrite(ctx, "PUT", putURL, body)
	}
	postURL := fmt.Sprintf("https://api.cloudflare.com/client/v4/zones/%s/dns_records", zoneID)
	return u.cfWrite(ctx, "POST", postURL, body)
}

func (u *Updater) cfGet(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+u.apiToken)
	resp, err := u.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("cloudflare API returned %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (u *Updater) cfWrite(ctx context.Context, method, url, body string) error {
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+u.apiToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := u.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 && resp.StatusCode != 201 {
		b, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("cloudflare API returned %d: %s", resp.StatusCode, b)
	}
	return nil
}
