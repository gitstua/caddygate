# CaddyGate

> [!CAUTION]
> This project is experimental. Use at own risk as it will likely break. It does not work on MacOS with Docker as the networking stack presents the wrong IP to the filtering

A zero-trust reverse proxy for Docker workloads. Caddy handles TLS (on-demand certs via DNS-01 ACME). A Go sidecar handles IP enrollment and Docker service discovery. No database — the allowlist lives directly in Caddy's config and survives restarts.

All traffic is denied by default. Users enroll their IP by visiting a secret URL. Docker containers are discovered automatically and get a subdomain — no config edits needed.

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
    image: ghcr.io/gitstua/caddygate:latest   # Cloudflare DNS
    # image: ghcr.io/gitstua/caddygate:latest-route53   # AWS Route53
    # image: ghcr.io/gitstua/caddygate:latest-azure     # Azure DNS
    restart: unless-stopped
    ports:
      - "80:80"
      - "443:443"
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
      - caddy_data:/data
      - caddy_config:/config
    env_file: .env
    networks:
      - caddygate

volumes:
  caddy_data:
  caddy_config:

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

Caddy provisions TLS certificates on demand via DNS-01 ACME — each subdomain gets its own cert the first time it is accessed. No pre-provisioning needed.

**4. Enroll your IP**

Open the admin page on your LAN and scan the QR code with your mobile device:

```
http://<host-ip>:7080
```

The enrollment secret URL is never displayed publicly. Keep it private — anyone with it can add their IP to your allowlist.

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

---

## Available images

| Tag | DNS provider |
|---|---|
| `latest`, `latest-cloudflare` | Cloudflare |
| `latest-route53` | AWS Route 53 |
| `latest-azure` | Azure DNS |

Versioned tags (`v1.2.3`, `v1.2.3-cloudflare`, etc.) are published on each release. Other DNS providers can be used by building from source — see the [DNS provider table](#dns-provider) below.

---

## DNS provider

| Provider | Module | Image tag |
|---|---|---|
| Cloudflare | `github.com/caddy-dns/cloudflare` | `latest-cloudflare` |
| Route53 | `github.com/caddy-dns/route53` | `latest-route53` |
| Azure DNS | `github.com/caddy-dns/azure` | `latest-azure` |
| Hetzner | `github.com/caddy-dns/hetzner` | build from source |
| Namecheap | `github.com/caddy-dns/namecheap` | build from source |

**Token scoping (Cloudflare):** Create an API token scoped to *Zone / DNS / Edit* for the specific zone only. Never use a Global API Key.

**Token scoping (Route53):** IAM policy restricted to `route53:ChangeResourceRecordSets` and `route53:ListResourceRecordSets` on the specific hosted zone ARN.

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
| `CADDYGATE_DNS_PROVIDER` | — | `cloudflare`, `route53`, `azure`, etc. |
| `CADDYGATE_DNS_API_TOKEN` | — | DNS provider credential |
| `CADDYGATE_ENROLLMENT_SECRET` | — | Secret for the enrollment URL (keep private) |
| `CADDYGATE_INITIAL_CIDRS` | — | Comma-separated CIDRs pre-seeded on startup |
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

To remove an IP from the allowlist:

```sh
# View current allowlist
docker exec <caddygate-container> \
  curl --unix-socket /run/caddy/admin.sock \
  http://caddy/config/apps/http/servers/srv0/routes/1/match/0/remote_ip/ranges

# Update the list (replace with the array minus the IP to remove)
docker exec <caddygate-container> \
  curl --unix-socket /run/caddy/admin.sock \
  -X PATCH -H "Content-Type: application/json" \
  http://caddy/config/apps/http/servers/srv0/routes/1/match/0/remote_ip/ranges \
  -d '["203.0.113.1/32"]'
```

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
│   ├── allowlist/seed.go          # seeds initial CIDRs into Caddy on startup
│   ├── caddy/client.go            # Caddy Admin API client (unix socket)
│   ├── config/config.go           # env var parsing
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
