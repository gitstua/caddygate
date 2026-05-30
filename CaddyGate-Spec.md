# CaddyGate — Technical Specification

**Version:** 0.2-draft  
**Stack:** Go (sidecar), Caddy, Docker, ACME DNS challenge  
**Inspiration:** TSDProxy  

---

## 1. Overview

CaddyGate is a self-hosted, security-first reverse proxy that sits in front of Docker workloads. It uses Caddy as its core engine to handle TLS automatically via DNS-01 ACME challenge (wildcard certs), discovers Docker containers by label, builds DNS routes dynamically, and enforces an IP allowlist that users join via a shared enrollment URL.

**Design goals:**
- Zero-trust by default — all traffic denied unless IP is on the allowlist
- Wildcard TLS for the entire `*.apps.example.com` domain with no manual cert work
- Frictionless user enrollment via a single pre-shared UUID link — no database, no accounts
- Automatic subdomain creation from container labels — no config file edits
- Allowlist survives restarts via Caddy's native config persistence (`--resume`)

---

## 2. Architecture

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

## 3. Components

### 3.1 TLS Provisioning

**Mechanism:** ACME DNS-01 challenge via Caddy's `tls` directive.

Caddy natively supports wildcard certificates using DNS-01 — no HTTP challenge, no port 80 exposure required. The operator configures DNS provider credentials once; Caddy handles issuance and renewal automatically.

**Caddyfile fragment:**
```caddyfile
*.apps.example.com {
    tls {
        dns <provider> {
            api_token {env.DNS_API_TOKEN}
        }
    }
}
```

**Supported DNS providers** (via `caddy-dns/*` modules): Cloudflare, Route53, Azure DNS, Hetzner, Namecheap, and others. Provider is selected by environment variable at deploy time.

**Certificate storage:** Caddy's on-disk storage (mounted Docker volume) — certs persist across container restarts automatically.

---

### 3.2 IP Allowlist

The allowlist is stored directly in Caddy's live JSON config as a `remote_ip` matcher. There is no separate database. Caddy persists its config to disk automatically when run with `--resume`, so the allowlist survives restarts.

**How it works:**

Caddy has a named matcher in its config:
```json
{
  "match": [{
    "remote_ip": {
      "ranges": ["203.0.113.42/32", "198.51.100.0/24"]
    }
  }]
}
```

When a user enrolls, CaddyGate calls the Caddy Admin API to append their IP to this list:
```
PATCH /config/apps/http/servers/srv0/routes/0/match/0/remote_ip/ranges
```

Caddy atomically applies the change and auto-saves it to `$CADDY_DATA/config/autosave.json`. On next `caddy run --resume`, the saved config (including all enrolled IPs) is restored.

**Deny response:** `HTTP 403 Forbidden` with a plain-text body:
```
Access denied. Request enrollment at: https://apps.example.com/hello-its-me/<your-token>
```

**XFF trust:** Configurable via `CADDYGATE_TRUSTED_PROXIES` env var. Accepts CIDR list. Defaults to direct IP only (no XFF trust) for maximum safety.

---

### 3.3 Enrollment Page

A lightweight HTTP handler at `https://apps.example.com/hello-its-me/{uuid}`.

The UUID is a single value set in `.env` — no token table, no generation logic. The admin shares the URL once; anyone with it can enroll their current IP.

**`.env` entry:**
```
CADDYGATE_ENROLLMENT_UUID=f47ac10b-58cc-4372-a567-0e02b2c3d479
```

**Flow:**
```
1. Admin sets CADDYGATE_ENROLLMENT_UUID in .env and shares the URL
2. User visits https://apps.example.com/hello-its-me/<uuid>
3. UUID matches env var → CaddyGate extracts user's public IP
4. PATCH request to Caddy Admin API appends IP to remote_ip ranges
5. Caddy persists updated config to disk (--resume ensures survival across restarts)
6. User sees success page
```

**Caddy route ordering** — enrollment path is excluded from the allowlist matcher so unenrolled users can reach it:

```caddyfile
*.apps.example.com {
    log {
        output file /var/log/caddy/access.log
        format filter {
            wrap json
            fields {
                request>uri replace /hello-its-me/.* /hello-its-me/[redacted]
            }
        }
    }

    # Enrollment — outside allowlist, UUID redacted in logs
    @enrollment path /hello-its-me/*
    handle @enrollment {
        reverse_proxy caddygate-sidecar:8081
    }

    # Everything else requires an allowlisted IP
    @allowed remote_ip {env.CADDYGATE_INITIAL_CIDRS}
    handle @allowed {
        reverse_proxy ...  # dynamic upstreams from discovery
    }

    handle {
        respond "Access denied." 403
    }
}
```

**Security considerations:**
- UUID is set once in `.env` and never changes unless the admin rotates it
- UUID v4 gives 122 bits of entropy — brute-force not feasible
- UUID in path position avoids leakage via query string in server logs, browser history, and HTTP `Referer` headers
- Caddy log filter redacts the UUID from access logs before writing to disk
- Rate-limit `/hello-its-me/*`: max 10 attempts/minute per IP, regardless of UUID match
- Any UUID mismatch, malformed path, or missing UUID returns a plain `404 Not Found` — identical response to any unknown route. No error message, no body, no indication the path is meaningful. This prevents path enumeration and makes the enrollment surface invisible to scanners.
- If the enrolling IP is already in Caddy's `remote_ip` ranges, respond 200 "already enrolled" — do not error
- To revoke access, remove the IP from Caddy's config via the Admin API and rotate the UUID in `.env`

**Response page:** Minimal HTML (no external assets, no JS). Shows:
- Success: "You've been added. You can now access the services."
- Already enrolled: "Your IP is already on the access list."
- Invalid UUID / no UUID segment / any other failure: `404 Not Found` — identical to any unknown path. No body, no indication the path is meaningful.

---

### 3.4 Docker Service Discovery

CaddyGate watches the Docker daemon socket (`/var/run/docker.sock`) for container start/stop events and dynamically updates Caddy's reverse proxy routes via the Admin API.

**Label schema:**

| Label | Required | Example | Description |
|---|---|---|---|
| `caddygate.enable` | Yes | `true` | Opt container in |
| `caddygate.name` | Yes | `app1` | Subdomain prefix → `app1.apps.example.com` |
| `caddygate.port` | Yes | `8080` | Container port to proxy to |
| `caddygate.strip_prefix` | No | `/api` | Strip path prefix before forwarding |
| `caddygate.health_path` | No | `/health` | Path for upstream health probes |

**Discovery loop:**
```
1. On startup: enumerate all running containers with caddygate.enable=true
2. Register each as a Caddy upstream via Admin API
3. Subscribe to Docker events: container start → register, container stop → deregister
4. All changes are atomic Caddy config patches — no restart, no dropped connections
5. Caddy auto-saves after each patch; routes survive restarts via --resume
```

**DNS resolution:** Containers are addressed by their Docker network alias or container name. CaddyGate must share a Docker network with the target containers. The sidecar resolves `app1` → container IP internally.

**Generated Caddy route (pushed via Admin API, not written to Caddyfile):**
```json
{
  "match": [{"host": ["app1.apps.example.com"]}],
  "handle": [{
    "handler": "reverse_proxy",
    "upstreams": [{"dial": "app1:8080"}],
    "health_checks": {
      "active": {"uri": "/health", "interval": "10s"}
    }
  }]
}
```

---

## 4. Configuration

All configuration via `.env` / environment variables. A `docker-compose.yml` example:

```yaml
services:
  caddygate:
    image: ghcr.io/yourorg/caddygate:latest
    command: caddy run --resume --config /config/caddy.json
    ports:
      - "443:443"
      - "80:80"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - caddy_data:/data        # certs + autosaved config
      - caddy_config:/config    # base caddy.json template
    env_file: .env

volumes:
  caddy_data:
  caddy_config:
```

**`.env` reference:**

| Variable | Default | Description |
|---|---|---|
| `CADDYGATE_BASE_DOMAIN` | — | Wildcard domain, e.g. `apps.example.com` |
| `CADDYGATE_DNS_PROVIDER` | — | `cloudflare`, `route53`, `azure`, etc. |
| `CADDYGATE_DNS_API_TOKEN` | — | DNS provider credential |
| `CADDYGATE_ENROLLMENT_UUID` | — | Single UUID for the enrollment URL |
| `CADDYGATE_INITIAL_CIDRS` | — | Comma-separated CIDRs pre-seeded into allowlist (e.g. admin IP) |
| `CADDYGATE_TRUSTED_PROXIES` | `""` | CIDRs to trust for `X-Forwarded-For`, space-separated |
| `CADDYGATE_ENROLL_RATE_LIMIT` | `10` | Max enrollment attempts/minute per IP |
| `CADDYGATE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `CADDYGATE_ADMIN_SOCKET` | `/run/caddy/admin.sock` | Caddy Admin API socket path |

---

## 5. Persistence Model

| Data | Storage | Survives restart? |
|---|---|---|
| TLS certificates | `caddy_data` Docker volume | Yes |
| Enrolled IPs (allowlist) | Caddy autosave (`caddy_data/config/autosave.json`) via `--resume` | Yes |
| Discovered routes | Caddy autosave, re-synced from Docker on startup | Yes (re-discovered on start) |
| Enrollment UUID | `.env` file | Yes |

No external database. No SQLite. No Postgres.

---

## 6. Security Model

| Threat | Mitigation |
|---|---|
| Unenrolled user accesses services | `remote_ip` matcher in Caddy; 403 before upstream contact |
| Token leakage via logs/referrer | UUID in path position; Caddy log filter redacts `/hello-its-me/*` URIs |
| Token enumeration | UUID v4 entropy + rate limiting on `/hello-its-me/*` |
| IP spoofing via XFF | XFF trust disabled by default; opt-in via `CADDYGATE_TRUSTED_PROXIES` |
| Docker socket abuse | Mounted read-only; discovery uses minimal API surface |
| MITM on internal traffic | All upstream traffic over Docker internal network |
| Wildcard cert compromise | Certs on volume only; DNS token scoped to TXT records |
| Admin API abuse | Admin API on Unix socket only; never exposed on network |
| UUID rotation | Change `CADDYGATE_ENROLLMENT_UUID` in `.env` and restart sidecar |

### Recommended DNS token scoping

For Cloudflare: API token scoped to **Zone / DNS / Edit** for the specific zone only. Never a Global API Key.

For Route53: IAM policy restricted to `route53:ChangeResourceRecordSets` and `route53:ListResourceRecordSets` on the specific hosted zone ARN.

---

## 7. Implementation Plan

### Phase 1 — Core proxy + TLS
- Caddy with wildcard TLS via DNS-01
- Static `remote_ip` matcher with `CADDYGATE_INITIAL_CIDRS`
- `caddy run --resume` wired up; verify config persists across restart
- Verify cert issuance and renewal end-to-end

### Phase 2 — Enrollment
- Enrollment HTTP handler reading UUID from env
- PATCH to Caddy Admin API on successful match
- Rate limiting on `/hello-its-me/*`
- Log redaction for enrollment path
- Minimal enrollment response page

### Phase 3 — Docker discovery
- Docker event listener
- Label parsing and Caddy Admin API route registration
- Atomic deregistration on container stop
- Health probe support

### Phase 4 — Hardening
- Audit log for allowlist changes (append-only flat file)
- `/status` page (behind allowlist) showing upstream health
- UUID rotation helper script
- Rate limit metrics endpoint

---

## 8. File Structure

```
caddygate/
├── cmd/
│   └── caddygate/          # main entrypoint (sidecar process)
├── internal/
│   ├── allowlist/          # Caddy Admin API patch for remote_ip ranges
│   ├── discovery/          # Docker event loop + route registration
│   ├── enrollment/         # /hello-its-me/{uuid} handler
│   ├── caddy/              # Admin API client
│   └── config/             # env var parsing
├── web/
│   └── enroll.html         # enrollment response page
├── caddy.json.tmpl         # base Caddy config template (seeded on first run)
├── .env.example
├── docker-compose.yml
└── Dockerfile
```

---

*Spec status: draft for review. Phase 1 can begin once DNS provider and base domain are confirmed.*
