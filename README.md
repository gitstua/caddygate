# CaddyGate

> [!CAUTION]
> This project is experimental. Use at own risk as it will likely break. It does not work on MacOS with Docker as the networking stack presents the wrong IP to the filtering

A zero-trust reverse proxy for Docker workloads and local network services. Caddy handles TLS (certs provisioned at startup via DNS-01 ACME). A Go sidecar handles IP enrollment, Docker service discovery, and optional DDNS updates. No database — the allowlist lives directly in Caddy's config and survives restarts.

All traffic is denied by default. Users enroll their IP by visiting a secret URL. Docker containers are discovered automatically and get a subdomain — no config edits needed. Non-Docker services on your local network can also be proxied via environment config.

---

## Quick start (pre-built image)

No cloning or building required. You need Docker, a domain with DNS managed by a supported provider, and an API token for that provider.

**1. Create a working directory and your compose file**

```sh
mkdir caddygate && cd caddygate
```

Create `docker-compose.yml`:

```yaml
services:
  caddygate:
    image: ghcr.io/gitstua/caddygate:latest
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
      - "127.0.0.1:7080:7080"  # admin page — LAN only, do not expose to internet
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - ./caddy_data:/data
      - ./caddy_config:/config
    env_file: .env
    networks:
      - caddygate

networks:
  caddygate:
    driver: bridge
```

**2. Configure**

Create `.env`:

```sh
CADDYGATE_BASE_DOMAIN=apps.example.com
CADDYGATE_DNS_PROVIDER=cloudflare
CADDYGATE_DNS_API_TOKEN=your-dns-api-token-here
CADDYGATE_ENROLLMENT_SECRET=      # generate: python3 -c "import uuid; print(uuid.uuid4())"
CADDYGATE_INITIAL_CIDRS=203.0.113.42/32    # your current IP — prevents lockout on first boot
```

Your DNS provider must be pointed at this machine. Point a wildcard DNS record `*.apps.example.com` at your public IP before starting.

**3. Start**

```sh
docker compose up -d
```

Caddy provisions TLS certificates at startup via DNS-01 ACME for all known subdomains (enrollment URL, static services, and Docker containers as they start). No on-demand cert issuance — only registered subdomains get certificates.

**4. Enroll your IP**

Open the admin page on your LAN and scan the QR code with your mobile device:

```
http://<host-ip>:7080
```

The admin page also shows all enrolled IPs (with a delete button) and all managed service URLs. The enrollment secret URL is never displayed publicly. Keep it private — anyone with it can add their IP to your allowlist.

**5. Add an app**

CaddyGate handles internet access — TLS, subdomain, and allowlist enforcement. LAN access by IP:port is separate and works independently; just add a `ports:` mapping as you normally would.

```yaml
services:
  myapp:
    image: myapp:latest
    ports:
      - "8080:8080"          # optional: direct LAN access at 192.168.x.x:8080
    labels:
      caddygate.name: "myapp"
      caddygate.port: "8080" # internet access at https://myapp.apps.example.com
    networks:
      - caddygate

networks:
  caddygate:
    external: true
```

The `caddygate.name` label is all that's needed to enable discovery. Within seconds the app is reachable at `https://myapp.apps.example.com` for any enrolled IP — no restart, no config edits.

Port forwarding individual container ports from your router to your LAN is your responsibility. CaddyGate only manages the HTTPS reverse proxy layer.

**5. Add non-Docker services (optional)**

To proxy local network services that aren't Docker containers (e.g. a NAS, Home Assistant, or any `host:port`), set `CADDYGATE_STATIC_SERVICES` in your `.env`:

```sh
CADDYGATE_STATIC_SERVICES=homeassistant=http://192.168.50.193:8123,nas=http://192.168.50.10:5000
```

Each entry is `name=upstream` where upstream is a URL or bare `host:port`. The subdomain `name.apps.example.com` is registered and its cert provisioned at startup. These services appear alongside Docker-discovered services on the admin page.

---

## DNS provider

Cloudflare only. Create an API token scoped to *Zone / DNS / Edit* for the specific zone. Never use a Global API Key.

The same token is used for both ACME DNS challenges and optional DDNS updates — no separate credentials needed.

Versioned tags (`v1.2.3`) are published on each release alongside `latest`.

---

## Container labels

| Label | Required | Example | Description |
|---|---|---|---|
| `caddygate.name` | Yes | `app1` | Subdomain prefix → `app1.apps.example.com`. Presence of this label enables discovery. |
| `caddygate.port` | No | `8080` | Container port to proxy to (default: `80`) |
| `caddygate.strip_prefix` | No | `/api` | Strip path prefix before forwarding |
| `caddygate.health_path` | No | `/health` | Path for active health probes |

---

## Configuration reference

All config via `.env`:

| Variable | Default | Description |
|---|---|---|
| `CADDYGATE_BASE_DOMAIN` | — | Wildcard domain, e.g. `apps.example.com` |
| `CADDYGATE_DNS_API_TOKEN` | — | Cloudflare API token (Zone / DNS / Edit) |
| `CADDYGATE_ENROLLMENT_SECRET` | — | Secret for the enrollment URL (keep private) |
| `CADDYGATE_INITIAL_CIDRS` | — | Comma-separated CIDRs pre-seeded on startup |
| `CADDYGATE_STATIC_SERVICES` | — | Comma-separated `name=upstream` pairs for non-Docker services |
| `CADDYGATE_DDNS_ENABLED` | `false` | Set `true` to keep `*.basedomain` pointed at the current public IP |
| `CADDYGATE_DDNS_INTERVAL` | `5m` | How often to check and update the DNS record |
| `CADDYGATE_TRUSTED_PROXIES` | `""` | CIDRs to trust for `X-Forwarded-For` |
| `CADDYGATE_ENROLL_RATE_LIMIT` | `10` | Max enrollment attempts/min per IP |
| `CADDYGATE_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `CADDYGATE_ADMIN_SOCKET` | `/run/caddy/admin.sock` | Caddy Admin API socket path |
| `CADDYGATE_LISTEN_ADDR` | `:8081` | Sidecar HTTP listen address (internal) |
| `CADDYGATE_ADMIN_ADDR` | `:7080` | Admin page address — access via `http://<host-ip>:7080` |

---

## Rotating the enrollment secret

To revoke enrollment access (existing IPs remain, new ones can't enroll via the old URL):

```sh
# Generate a new secret
python3 -c "import uuid; print(uuid.uuid4())"

# Update CADDYGATE_ENROLLMENT_SECRET in .env, then restart only the sidecar
# (Caddy keeps running, allowlist unaffected)
docker compose restart caddygate
```

To remove an IP from the allowlist, click the ✕ button next to it on the admin page (`http://<host-ip>:7080`).

---

## Persistence

| Data | Where | Survives restart |
|---|---|---|
| TLS certificates | `caddy_data` Docker volume | Yes |
| Enrolled IPs | Caddy autosave in `caddy_config` via `--resume` | Yes |
| Discovered routes | Caddy autosave; re-synced from Docker on startup | Yes |
| Enrollment secret | `.env` file | Yes |

No external database. No SQLite. No Postgres.

---

## Security notes

- All unknown paths (including wrong or missing secrets at `/hello-its-me/`) return `404` — identical to any unknown route. No body, no indication the endpoint exists.
- The enrollment secret is redacted from Caddy access logs before writing to disk.
- Rate limiting fires before UUID comparison — prevents timing-based enumeration.
- XFF trust is disabled by default. Enable only if you have an upstream load balancer you control.
- The Caddy Admin API is on a Unix socket only — never exposed on a network interface.

---

## Development

See [CaddyGate-Spec.md](CaddyGate-Spec.md) for the full technical specification and architecture.

### Building from source

```sh
git clone https://github.com/gitstua/caddygate
cd caddygate
cp .env.example .env  # fill in your values
docker compose up -d --build
```

To build with a different DNS provider:

```sh
docker compose build --build-arg DNS_MODULE=github.com/caddy-dns/route53
```

### Project structure

```
caddygate/
├── cmd/caddygate/main.go          # entrypoint — wires everything together
├── internal/
│   ├── admin/handler.go           # admin page: QR code, IP list, service links
│   ├── allowlist/seed.go          # seeds initial CIDRs into Caddy on startup
│   ├── caddy/client.go            # Caddy Admin API client (unix socket)
│   ├── config/config.go           # env var parsing
│   ├── ddns/updater.go            # optional Cloudflare DDNS updater
│   ├── discovery/agent.go         # Docker event loop + route registration
│   └── enrollment/handler.go      # /hello-its-me/{secret} HTTP handler
├── .github/workflows/publish.yml  # builds and publishes images to GHCR
├── caddy.json.tmpl                # base Caddy config (written on first boot)
├── entrypoint.sh                  # stamps template, starts Caddy + sidecar
├── Dockerfile                     # multi-stage: xcaddy + Go build
├── docker-compose.yml             # development compose (builds locally)
├── .env.example
└── README.md
```
