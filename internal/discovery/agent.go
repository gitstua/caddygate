package discovery

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/gitstua/caddygate/internal/caddy"
)

const dockerSocket = "/var/run/docker.sock"

// Agent watches the Docker socket and registers/deregisters Caddy routes.
type Agent struct {
	baseDomain string
	caddy      *caddy.Client
	log        *slog.Logger
	http       *http.Client
}

func NewAgent(baseDomain string, caddyClient *caddy.Client, log *slog.Logger) *Agent {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", dockerSocket)
		},
	}
	return &Agent{
		baseDomain: baseDomain,
		caddy:      caddyClient,
		log:        log,
		http:       &http.Client{Transport: transport, Timeout: 30 * time.Second},
	}
}

// containerInfo holds the labels we care about from a Docker container inspect.
type containerInfo struct {
	Name   string
	Port   string
	Health string
}

type dockerContainer struct {
	ID     string            `json:"Id"`
	Names  []string          `json:"Names"`
	Labels map[string]string `json:"Labels"`
	State  string            `json:"State"`
}

type dockerEvent struct {
	Type   string `json:"Type"`
	Action string `json:"Action"`
	Actor  struct {
		Attributes map[string]string `json:"Attributes"`
	} `json:"Actor"`
	ID string `json:"id"`
}

// Run bootstraps existing containers then listens for events. Blocks until ctx is cancelled.
func (a *Agent) Run(ctx context.Context) error {
	a.log.Info("discovery agent starting")

	if err := a.syncExisting(ctx); err != nil {
		a.log.Warn("initial sync failed", "err", err)
	}

	return a.watchEvents(ctx)
}

func (a *Agent) syncExisting(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, "GET", "http://docker/containers/json?all=false", nil)
	if err != nil {
		return err
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return fmt.Errorf("list containers: %w", err)
	}
	defer resp.Body.Close()

	var containers []dockerContainer
	if err := json.NewDecoder(resp.Body).Decode(&containers); err != nil {
		return fmt.Errorf("decode containers: %w", err)
	}

	for _, c := range containers {
		info := parseLabels(c.Labels)
		if info.Name == "" {
			continue
		}
		host := info.Name + "." + a.baseDomain
		upstream := info.Name + ":" + info.Port
		if err := a.caddy.UpsertRoute(host, upstream, info.Health); err != nil {
			a.log.Error("upsert route", "host", host, "err", err)
		} else {
			a.log.Info("registered container", "host", host, "upstream", upstream)
		}
		if err := a.caddy.ProvisionCert(host); err != nil {
			a.log.Warn("cert provision failed", "host", host, "err", err)
		}
	}
	return nil
}

func (a *Agent) watchEvents(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		req, err := http.NewRequestWithContext(ctx, "GET",
			"http://docker/events?filters=%7B%22type%22%3A%7B%22container%22%3Atrue%7D%7D", nil)
		if err != nil {
			return err
		}

		// Use a longer timeout for the streaming events endpoint
		streamClient := &http.Client{
			Transport: a.http.Transport,
			Timeout:   0, // no timeout — this is a long-lived stream
		}
		resp, err := streamClient.Do(req)
		if err != nil {
			a.log.Warn("docker events stream error, retrying in 5s", "err", err)
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				continue
			}
		}

		a.log.Info("connected to docker event stream")
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			var event dockerEvent
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				continue
			}
			if event.Type != "container" {
				continue
			}
			a.handleEvent(event)
		}
		resp.Body.Close()

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(2 * time.Second):
		}
	}
}

func (a *Agent) handleEvent(event dockerEvent) {
	labels := event.Actor.Attributes
	info := parseLabels(labels)

	switch event.Action {
	case "start":
		if info.Name == "" {
			return
		}
		host := info.Name + "." + a.baseDomain
		upstream := info.Name + ":" + info.Port
		if err := a.caddy.UpsertRoute(host, upstream, info.Health); err != nil {
			a.log.Error("upsert route on start", "host", host, "err", err)
		} else {
			a.log.Info("container started, route registered", "host", host, "upstream", upstream)
		}
		if err := a.caddy.ProvisionCert(host); err != nil {
			a.log.Warn("cert provision failed", "host", host, "err", err)
		}

	case "die", "stop", "kill":
		if info.Name == "" {
			return
		}
		host := info.Name + "." + a.baseDomain
		if err := a.caddy.RemoveRoute(host); err != nil {
			a.log.Error("remove route on stop", "host", host, "err", err)
		} else {
			a.log.Info("container stopped, route removed", "host", host)
		}
	}
}

func parseLabels(labels map[string]string) containerInfo {
	info := containerInfo{
		Port: "80", // sensible default
	}
	for k, v := range labels {
		switch k {
		case "caddygate.name":
			info.Name = v
		case "caddygate.port":
			info.Port = v
		case "caddygate.health_path":
			info.Health = v
		}
	}
	return info
}
