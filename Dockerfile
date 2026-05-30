# Stage 1: build the CaddyGate sidecar
FROM golang:1.22-alpine AS builder

WORKDIR /src
COPY go.mod ./
RUN go mod download || true
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /caddygate ./cmd/caddygate

# Stage 2: final image — caddy + sidecar binary
# Use the official Caddy image with the dns provider modules you need.
# Build a custom Caddy with xcaddy to include your DNS provider:
#
#   docker build \
#     --build-arg DNS_MODULE=github.com/caddy-dns/cloudflare \
#     -t ghcr.io/yourorg/caddygate:latest .
#
ARG DNS_MODULE=github.com/caddy-dns/cloudflare

FROM caddy:2-builder AS caddy-builder
ARG DNS_MODULE
RUN xcaddy build --with ${DNS_MODULE}

FROM caddy:2-alpine

# Copy custom Caddy binary (with DNS module)
COPY --from=caddy-builder /usr/bin/caddy /usr/bin/caddy

# Copy sidecar binary and supporting files
COPY --from=builder /caddygate       /app/caddygate
COPY caddy.json.tmpl                 /app/caddy.json.tmpl
COPY entrypoint.sh                   /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh /app/caddygate

# Caddy data + config volumes (certs and autosaved config persist here)
VOLUME ["/data", "/config"]

EXPOSE 80 443

ENTRYPOINT ["/app/entrypoint.sh"]
