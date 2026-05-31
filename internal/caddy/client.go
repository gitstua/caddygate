package caddy

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Client talks to the Caddy Admin API over a Unix socket.
type Client struct {
	http       *http.Client
	socketPath string
}

func NewClient(socketPath string) *Client {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
		},
	}
	return &Client{
		socketPath: socketPath,
		http: &http.Client{
			Transport: transport,
			Timeout:   10 * time.Second,
		},
	}
}

func (c *Client) do(method, path string, body any) ([]byte, int, error) {
	var bodyReader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, fmt.Errorf("marshal: %w", err)
		}
		bodyReader = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, "http://caddy"+path, bodyReader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("caddy admin request: %w", err)
	}
	defer resp.Body.Close()

	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return b, resp.StatusCode, nil
}

// findRemoteIPRouteIndex returns the index of the top-level route containing a remote_ip matcher.
func (c *Client) findRemoteIPRouteIndex() (int, error) {
	b, status, err := c.do("GET", "/config/apps/http/servers/srv0/routes", nil)
	if err != nil {
		return -1, err
	}
	if status != 200 {
		return -1, fmt.Errorf("caddy returned %d fetching routes", status)
	}

	var routes []struct {
		Match []struct {
			RemoteIP *struct {
				Ranges []string `json:"ranges"`
			} `json:"remote_ip,omitempty"`
		} `json:"match,omitempty"`
	}
	if err := json.Unmarshal(b, &routes); err != nil {
		return -1, fmt.Errorf("unmarshal routes: %w", err)
	}
	for i, route := range routes {
		for _, m := range route.Match {
			if m.RemoteIP != nil {
				return i, nil
			}
		}
	}
	return -1, fmt.Errorf("no remote_ip route found in caddy config")
}

// GetAllowedRanges returns the current list of allowed IP CIDRs from Caddy's config.
func (c *Client) GetAllowedRanges() ([]string, error) {
	idx, err := c.findRemoteIPRouteIndex()
	if err != nil {
		return nil, err
	}
	path := fmt.Sprintf("/config/apps/http/servers/srv0/routes/%d/match/0/remote_ip/ranges", idx)
	b, status, err := c.do("GET", path, nil)
	if err != nil {
		return nil, err
	}
	if status == 404 {
		return nil, nil
	}
	if status != 200 {
		return nil, fmt.Errorf("caddy returned %d: %s", status, b)
	}
	var ranges []string
	if err := json.Unmarshal(b, &ranges); err != nil {
		return nil, fmt.Errorf("unmarshal ranges: %w", err)
	}
	return ranges, nil
}

// AddAllowedRange appends a CIDR to the allowlist in Caddy's live config.
func (c *Client) AddAllowedRange(cidr string) error {
	idx, err := c.findRemoteIPRouteIndex()
	if err != nil {
		return fmt.Errorf("find remote_ip route: %w", err)
	}

	existing, err := c.GetAllowedRanges()
	if err != nil {
		return fmt.Errorf("get existing ranges: %w", err)
	}
	for _, r := range existing {
		if r == cidr {
			return nil
		}
	}

	updated := append(existing, cidr)
	path := fmt.Sprintf("/config/apps/http/servers/srv0/routes/%d/match/0/remote_ip/ranges", idx)
	_, status, err := c.do("PATCH", path, updated)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("caddy returned %d patching allowlist", status)
	}
	return nil
}

// RemoveAllowedRange removes a CIDR from the allowlist in Caddy's live config.
func (c *Client) RemoveAllowedRange(cidr string) error {
	idx, err := c.findRemoteIPRouteIndex()
	if err != nil {
		return fmt.Errorf("find remote_ip route: %w", err)
	}

	existing, err := c.GetAllowedRanges()
	if err != nil {
		return fmt.Errorf("get existing ranges: %w", err)
	}

	updated := make([]string, 0, len(existing))
	for _, r := range existing {
		if r != cidr {
			updated = append(updated, r)
		}
	}

	path := fmt.Sprintf("/config/apps/http/servers/srv0/routes/%d/match/0/remote_ip/ranges", idx)
	_, status, err := c.do("PATCH", path, updated)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("caddy returned %d patching allowlist", status)
	}
	return nil
}

// ProvisionCert asks Caddy to obtain and manage a certificate for the given host.
// This is needed because Caddy only auto-provisions certs for domains visible in
// top-level route host matchers; domains inside subroutes are not scanned.
func (c *Client) ProvisionCert(host string) error {
	_, status, err := c.do("POST", "/certificates/automate", []string{host})
	if err != nil {
		return err
	}
	if status != 200 && status != 201 {
		return fmt.Errorf("caddy returned %d automating cert for %s", status, host)
	}
	return nil
}

// IsAllowed checks whether a given IP is covered by any range in the allowlist.
func (c *Client) IsAllowed(ip string) (bool, error) {
	ranges, err := c.GetAllowedRanges()
	if err != nil {
		return false, err
	}
	for _, r := range ranges {
		if cidrContains(r, ip) {
			return true, nil
		}
	}
	return false, nil
}

// getSubroutes fetches the routes array inside the remote_ip subroute handler.
func (c *Client) getSubroutes() ([]json.RawMessage, string, error) {
	idx, err := c.findRemoteIPRouteIndex()
	if err != nil {
		return nil, "", err
	}
	path := fmt.Sprintf("/config/apps/http/servers/srv0/routes/%d/handle/0/routes", idx)
	b, status, err := c.do("GET", path, nil)
	if err != nil {
		return nil, "", err
	}
	if status != 200 {
		return nil, "", fmt.Errorf("caddy returned %d fetching subroutes", status)
	}
	var routes []json.RawMessage
	if err := json.Unmarshal(b, &routes); err != nil {
		return nil, "", fmt.Errorf("unmarshal subroutes: %w", err)
	}
	return routes, path, nil
}

// ManagedService holds display info for a proxied service.
type ManagedService struct {
	Host     string
	Upstream string // bare host:port dial address
}

// GetManagedServices returns all routes inside the remote_ip subroute with their upstreams.
func (c *Client) GetManagedServices() ([]ManagedService, error) {
	subroutes, _, err := c.getSubroutes()
	if err != nil {
		return nil, err
	}

	var parsed []struct {
		Match []struct {
			Host []string `json:"host"`
		} `json:"match"`
		Handle []struct {
			Upstreams []struct {
				Dial string `json:"dial"`
			} `json:"upstreams"`
		} `json:"handle"`
	}
	b, err := json.Marshal(subroutes)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &parsed); err != nil {
		return nil, fmt.Errorf("unmarshal subroutes: %w", err)
	}

	var services []ManagedService
	for _, route := range parsed {
		upstream := ""
		if len(route.Handle) > 0 && len(route.Handle[0].Upstreams) > 0 {
			upstream = route.Handle[0].Upstreams[0].Dial
		}
		for _, m := range route.Match {
			for _, host := range m.Host {
				services = append(services, ManagedService{Host: host, Upstream: upstream})
			}
		}
	}
	return services, nil
}

// UpsertRoute adds or replaces a reverse-proxy route inside the remote_ip subroute.
// This ensures all container routes are gated behind the allowlist.
func (c *Client) UpsertRoute(host, upstream, healthPath string) error {
	route := buildRoute(host, upstream, healthPath)

	existing, path, err := c.getSubroutes()
	if err != nil {
		return fmt.Errorf("get subroutes: %w", err)
	}

	// Remove existing route for this host, prepend the new one
	filtered := make([]json.RawMessage, 0, len(existing)+1)
	newRoute, err := json.Marshal(route)
	if err != nil {
		return err
	}
	filtered = append(filtered, newRoute)
	for _, r := range existing {
		if !strings.Contains(string(r), host) {
			filtered = append(filtered, r)
		}
	}

	_, status, err := c.do("PATCH", path, filtered)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("caddy returned %d upserting route for %s", status, host)
	}
	return nil
}

// RemoveRoute removes a reverse-proxy route from inside the remote_ip subroute.
func (c *Client) RemoveRoute(host string) error {
	existing, path, err := c.getSubroutes()
	if err != nil {
		return fmt.Errorf("get subroutes: %w", err)
	}

	filtered := make([]json.RawMessage, 0, len(existing))
	for _, r := range existing {
		if !strings.Contains(string(r), host) {
			filtered = append(filtered, r)
		}
	}

	_, status, err := c.do("PATCH", path, filtered)
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("caddy returned %d removing route for %s", status, host)
	}
	return nil
}

type caddyRoute struct {
	Match  []caddyHostMatch `json:"match"`
	Handle []caddyRPHandler `json:"handle"`
}

type caddyHostMatch struct {
	Host []string `json:"host"`
}

type caddyRPHandler struct {
	Handler      string          `json:"handler"`
	Upstreams    []caddyUpstream `json:"upstreams"`
	HealthChecks *caddyHealth    `json:"health_checks,omitempty"`
}

type caddyUpstream struct {
	Dial string `json:"dial"`
}

type caddyHealth struct {
	Active *caddyActiveHealth `json:"active,omitempty"`
}

type caddyActiveHealth struct {
	URI      string `json:"uri"`
	Interval string `json:"interval"`
}

func buildRoute(host, upstream, healthPath string) caddyRoute {
	handler := caddyRPHandler{
		Handler:   "reverse_proxy",
		Upstreams: []caddyUpstream{{Dial: upstream}},
	}
	if healthPath != "" {
		handler.HealthChecks = &caddyHealth{
			Active: &caddyActiveHealth{
				URI:      healthPath,
				Interval: "10s",
			},
		}
	}
	return caddyRoute{
		Match:  []caddyHostMatch{{Host: []string{host}}},
		Handle: []caddyRPHandler{handler},
	}
}

func cidrContains(cidr, ip string) bool {
	if cidr == ip || cidr == ip+"/32" || cidr == ip+"/128" {
		return true
	}
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