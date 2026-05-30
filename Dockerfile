# DNS_MODULE must be declared before the first FROM to be a true global default
ARG DNS_MODULE=github.com/caddy-dns/cloudflare

# Stage 1: build the CaddyGate sidecar
FROM golang:1.22-alpine AS builder

WORKDIR /src
COPY go.mod ./
RUN go mod tidy
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /caddygate ./cmd/caddygate

# Stage 2: build Caddy with the chosen DNS provider module
FROM caddy:2-builder AS caddy-builder
ARG DNS_MODULE
RUN xcaddy build --with ${DNS_MODULE}

# Stage 3: final image — caddy + sidecar binary
FROM caddy:2-alpine

COPY --from=caddy-builder /usr/bin/caddy /usr/bin/caddy
COPY --from=builder /caddygate       /app/caddygate
COPY caddy.json.tmpl                 /app/caddy.json.tmpl
COPY entrypoint.sh                   /app/entrypoint.sh
RUN chmod +x /app/entrypoint.sh /app/caddygate

VOLUME ["/data", "/config"]

EXPOSE 80 443

ENTRYPOINT ["/app/entrypoint.sh"]
