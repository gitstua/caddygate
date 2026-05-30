package admin

import (
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/yourorg/caddygate/internal/caddy"
)

// Handler serves the admin page showing the enrollment QR code and allowlist.
type Handler struct {
	enrollURL string
	caddy     *caddy.Client
	log       *slog.Logger
}

func NewHandler(baseDomain, uuid string, caddyClient *caddy.Client, log *slog.Logger) *Handler {
	return &Handler{
		enrollURL: fmt.Sprintf("https://%s/hello-its-me/%s", baseDomain, uuid),
		caddy:     caddyClient,
		log:       log,
	}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	png, err := qrcode.Encode(h.enrollURL, qrcode.Medium, 256)
	if err != nil {
		h.log.Error("admin: qr encode failed", "err", err)
		http.Error(w, "failed to generate QR code", http.StatusInternalServerError)
		return
	}
	qrDataURI := "data:image/png;base64," + base64.StdEncoding.EncodeToString(png)

	ranges, err := h.caddy.GetAllowedRanges()
	if err != nil {
		h.log.Warn("admin: could not fetch allowlist", "err", err)
	}

	var ipItems strings.Builder
	if len(ranges) == 0 {
		ipItems.WriteString("<li>No IPs enrolled yet.</li>")
	} else {
		for _, r := range ranges {
			fmt.Fprintf(&ipItems, "<li><code>%s</code></li>", r)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, page, qrDataURI, h.enrollURL, ipItems.String())
}

const page = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>CaddyGate Admin</title>
<style>
  body { font-family: system-ui, sans-serif; max-width: 560px; margin: 60px auto; padding: 0 24px; color: #1a1a1a; }
  h1 { font-size: 1.6rem; font-weight: 600; margin-bottom: 0; }
  h2 { font-size: 1.1rem; font-weight: 500; margin-top: 2rem; }
  img { display: block; margin: 16px 0; border: 1px solid #e0e0e0; border-radius: 8px; padding: 8px; background: #fff; }
  .url { font-family: monospace; font-size: 0.85rem; background: #f5f5f5; padding: 10px 14px; border-radius: 6px; word-break: break-all; }
  ul { padding-left: 1.2rem; }
  li { font-family: monospace; font-size: 0.9rem; margin: 4px 0; }
  code { background: #f5f5f5; padding: 1px 5px; border-radius: 3px; }
  .label { font-size: 0.75rem; color: #888; text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 6px; }
</style>
</head>
<body>
  <h1>CaddyGate</h1>

  <h2>Enrollment</h2>
  <p class="label">Scan to enroll your device's IP</p>
  <img src="%s" alt="Enrollment QR code" width="256" height="256">
  <div class="url">%s</div>

  <h2>Enrolled IPs</h2>
  <ul>%s</ul>
</body>
</html>`
