# CaddyGate

A zero-trust reverse proxy for Docker workloads. Caddy handles TLS (wildcard certs via DNS-01 ACME). A Go sidecar handles IP enrollment and Docker service discovery. No database — the allowlist lives directly in Caddy's config and survives restarts.

---

## How it works

1. All traffic is denied by default.
2. A user visits `https://apps.example.com/hello-its-me/<uuid>` — their IP is added to Caddy's allowlist instantly.
3. Docker containers with `caddygate.enable=true` labels are discovered automatically and get a subdomain: `app1.apps.example.com`.
4. Wildcard TLS for `*.apps.example.com` is provisioned and renewed automatically via DNS-01 ACME.
5. The allowlist persists across restarts — Caddy autosaves its config.

---

## Quickstart

**1. Clone and configure**

```sh
git clone https://github.com/yourorg/caddygate
cd caddygate
cp .env.example .env
```

Edit `.env`:

```sh
CADDYGATE_BASE_DOMAIN=apps.example.com
CADDYGATE_DNS_PROVIDER=cloudflare
CADDYGATE_DNS_API_TOKEN=your-token-here
CADDYGATE_ENROLLMENT_UUID=<generate with: python3 -c "import uuid; print(uuid.uuid4())">
CADDYGATE_INITIAL_CIDRS=<your.ip.address>/32
```

**2. Build and start**

```sh
docker compose up -d --build
```

**3. Enroll your IP**

Share this URL with anyone who needs access:

```
https://apps.example.com/hello-its-me/<your-uuid>
```

Visiting it adds the visitor's IP to the allowlist immediately.

**4. Add an app**

Add these labels to any container in the same Docker network:

```yaml
labels:
  caddygate.enable: "true"
  caddygate.name: "myapp"
  caddygate.port: "8080"
```

It becomes available at `https://myapp.apps.example.com` within seconds.

---

## DNS provider

The Dockerfile builds a custom Caddy binary with your DNS provider module. Change the `DNS_MODULE` build arg to match your provider:

| Provider | Module |
|---|---|
| Cloudflare | `github.com/caddy-dns/cloudflare` |
| Route53 | `github.com/caddy-dns/route53` |
| Azure DNS | `github.com/caddy-dns/azure` |
| Hetzner | `github.com/caddy-dns/hetzner` |
| Namecheap | `github.com/caddy-dns/namecheap` |

In `docker-compose.yml`:
```yaml
build:
  args:
    DNS_MODULE: github.com/caddy-dns/route53
```

**DNS token scoping (Cloudflare):** Create an API token scoped to *Zone / DNS / Edit* for the specific zone only. Never use a Global API Key.

**DNS token scoping (Route53):** IAM policy restricted to `route53:ChangeResourceRecordSets` and `route53:ListResourceRecordSets` on the specific hosted zone ARN.

---

## Container labels

| Label | Required | Example | Description |
|---|---|---|---|
| `caddygate.enable` | Yes | `true` | Opt container in to discovery |
| `caddygate.name` | Yes | `app1` | Subdomain prefix → `app1.apps.example.com` |
| `caddygate.port` | Yes | `8080` | Container port to proxy to |
| `caddygate.strip_prefix` | No | `/api` | Strip path prefix before forwarding |
| `caddygate.health_path` | No | `/health` | Path for active health probes |

---

## Configuration reference

All config via `.env`:

| Variable | Default | Description |
|---|---|---|
| `CADDYGATE_BASE_DOMAIN` | — | Wildcard domain, e.g. `apps.example.com` |
| `CADDYGATE_DNS_PROVIDER` | — | `cloudflare`, `route53`, `azure`, etc. |
| `CADDYGATE_DNS_API_TOKEN` | — | DNS provider credential |
| `CADDYGATE_ENROLLMENT_UUID` | — | UUID for the enrollment URL |
| `CADDYGATE_INITIAL_CIDRS` | — | Comma-separated CIDRs pre-seeded on startup |
| `CADDYGATE_TRUSTED_PROXIES` | `""` | CIDRs to trust for `X-Forwarded-For` |
| `CADDYGATE_ENROLL_RATE_LIMIT` | `10` | Max enrollment attempts/min per IP |
| `CADDYGATE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `CADDYGATE_ADMIN_SOCKET` | `/run/caddy/admin.sock` | Caddy Admin API socket path |
| `CADDYGATE_LISTEN_ADDR` | `:8081` | Sidecar HTTP listen address |

---

## Rotating the enrollment UUID

To revoke enrollment access (existing IPs remain, new ones can't enroll via the old URL):

```sh
# Generate a new UUID
python3 -c "import uuid; print(uuid.uuid4())"

# Update .env
CADDYGATE_ENROLLMENT_UUID=<new-uuid>

# Restart only the sidecar (Caddy keeps running, allowlist unaffected)
docker compose restart caddygate
```

To remove an IP from the allowlist, patch Caddy's config directly:

```sh
# View current allowlist
curl --unix-socket /run/caddy/admin.sock \
  http://caddy/config/apps/http/servers/srv0/routes/1/match/0/remote_ip/ranges

# Update the list (replace with the array minus the IP to remove)
curl --unix-socket /run/caddy/admin.sock \
  -X PATCH \
  -H "Content-Type: application/json" \
  http://caddy/config/apps/http/servers/srv0/routes/1/match/0/remote_ip/ranges \
  -d '["203.0.113.1/32"]'
```

---

## Persistence

| Data | Where | Survives restart |
|---|---|---|
| TLS certificates | `caddy_data` Docker volume | Yes |
| Enrolled IPs | Caddy autosave in `caddy_data` via `--resume` | Yes |
| Discovered routes | Caddy autosave; re-synced from Docker on startup | Yes |
| Enrollment UUID | `.env` file | Yes |

No external database. No SQLite. No Postgres.

---

## Security notes

- All unknown paths (including wrong or missing UUIDs at `/hello-its-me/`) return `404` — identical to any unknown route. No error body, no indication the path exists.
- The UUID is redacted from Caddy access logs before writing to disk.
- Rate limiting fires before UUID comparison — prevents timing-based enumeration.
- XFF trust is disabled by default. Enable only if you have an upstream load balancer you control.
- The Caddy Admin API is on a Unix socket only — never exposed on a network interface.

---

## Project structure

```
caddygate/
├── cmd/caddygate/main.go          # entrypoint — wires everything together
├── internal/
│   ├── allowlist/seed.go          # seeds initial CIDRs into Caddy on startup
│   ├── caddy/client.go            # Caddy Admin API client (unix socket)
│   ├── config/config.go           # env var parsing
│   ├── discovery/agent.go         # Docker event loop + route registration
│   └── enrollment/handler.go     # /hello-its-me/{uuid} HTTP handler
├── caddy.json.tmpl                # base Caddy config (written on first boot)
├── entrypoint.sh                  # stamps template, starts Caddy + sidecar
├── Dockerfile                     # multi-stage: xcaddy + Go build
├── docker-compose.yml
├── .env.example
└── README.md
```

---

## Technical specification

### Overview

CaddyGate is a self-hosted, security-first reverse proxy that sits in front of Docker workloads. It uses Caddy as its core engine to handle TLS automatically via DNS-01 ACME challenge (wildcard certs), discovers Docker containers by label, builds DNS routes dynamically, and enforces an IP allowlist that users join via a shared enrollment URL.

**Design goals:**
- Zero-trust by default — all traffic denied unless IP is on the allowlist
- Wildcard TLS for the entire `*.apps.example.com` domain with no manual cert work
- Frictionless user enrollment via a single pre-shared UUID link — no database, no accounts
- Automatic subdomain creation from container labels — no config file edits
- Allowlist survives restarts via Caddy's native config persistence (`--resume`)

---

### Architecture

```
Internet
    │  HTTPS :443
    ▼
┌─────────────────────────────────────────────────────┐
│  CaddyGate                                          │
│                                                     │
│  ┌──────────────┐      ┌──────────────────────────┐ │
│  │ TLS layer    │─────▶│ IP allowlist matcher      │ │
│  │ (Caddy ACME) │      │ remote_ip in Caddy config │ │
│  └──────────────┘      └──────────┬───────────────┘ │
│                                   │                 │
│                     ┌─────────────▼──────────────┐  │
│                     │ Caddy reverse proxy router  │  │
│                     │ *.apps.example.com matcher  │  │
│                     └──────┬──────────┬───────────┘  │
│                            │          │              │
│            ┌───────────────▼──┐  ┌────▼───────────┐ │
│            │ Discovery agent  │  │ Enrollment page │ │
│            │ Docker socket    │  │ UUID from .env  │ │
│            └───────────────┬──┘  └───────┬────────┘ │
└────────────────────────────│─────────────│──────────┘
                             │             │
              ┌──────────────┼──────┐      ▼
              ▼              ▼      ▼   PATCH Caddy Admin API
         app1:8080      app2:3000  appN  (add IP to remote_ip ranges)
```

---

### IP allowlist

Stored directly in Caddy's live JSON config as a `remote_ip` matcher. No separate database. Caddy persists its config to disk automatically when run with `--resume`.

When a user enrolls, the sidecar calls:
```
PATCH /config/apps/http/servers/srv0/routes/1/match/0/remote_ip/ranges
```

Caddy applies the change atomically and autosaves. On next `caddy run --resume`, the full allowlist is restored.

Deny response for non-enrolled IPs: `HTTP 403 Forbidden`.

---

### Enrollment

- Handler at `/hello-its-me/{uuid}`
- UUID is a single value from `.env` — no token table, no generation logic
- Any failure (wrong UUID, missing UUID, extra path segments) returns `404 Not Found` — identical to any unknown route, no body, reveals nothing
- Rate limited: max N attempts/minute per IP (fires before UUID comparison)
- UUID is redacted in Caddy access logs via log filter
- Already-enrolled IPs get a 200 "already enrolled" page

---

### Docker discovery

Watches `/var/run/docker.sock` for container start/stop events. On `start`, if `caddygate.enable=true`, registers a Caddy route via Admin API. On `stop`/`die`/`kill`, deregisters. All changes are atomic Caddy config patches — no restart, no dropped connections.

---

### Persistence model

| Data | Storage | Survives restart |
|---|---|---|
| TLS certificates | `caddy_data` Docker volume | Yes |
| Enrolled IPs | Caddy autosave via `--resume` | Yes |
| Discovered routes | Caddy autosave; re-synced from Docker on startup | Yes |
| Enrollment UUID | `.env` | Yes |

---

### Threat model

| Threat | Mitigation |
|---|---|
| Unenrolled user accesses services | `remote_ip` matcher in Caddy; 403 before upstream contact |
| Token leakage via logs/referrer | UUID in path position; Caddy log filter redacts `/hello-its-me/*` |
| Token enumeration | UUID v4 entropy + rate limiting before UUID check |
| Path existence leakage | All failures at `/hello-its-me/*` return identical 404 |
| IP spoofing via XFF | XFF trust disabled by default |
| Docker socket abuse | Mounted read-only; minimal API surface used |
| Admin API abuse | Unix socket only; never on network interface |
| UUID compromise | Rotate in `.env`, restart sidecar; existing IPs unaffected |
