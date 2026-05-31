package admin

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/gitstua/caddygate/internal/caddy"
)

// Handler serves the admin page showing the enrollment QR code and allowlist.
type Handler struct {
	enrollURL      string
	caddy          *caddy.Client
	certStorageDir string
	saveAllowlist  func()
	log            *slog.Logger
}

func NewHandler(baseDomain, secret string, caddyClient *caddy.Client, certStorageDir string, saveAllowlist func(), log *slog.Logger) *Handler {
	return &Handler{
		enrollURL:      fmt.Sprintf("https://enroll.%s/hello-its-me/%s", baseDomain, secret),
		caddy:          caddyClient,
		certStorageDir: certStorageDir,
		saveAllowlist:  saveAllowlist,
		log:            log,
	}
}

type certInfo struct {
	Host     string
	Expiry   time.Time
	DaysLeft int
	Issuer   string
}

// loadCerts scans the Caddy certificate storage directory and returns metadata
// for every cert found. Errors on individual certs are silently skipped.
func loadCerts(dir string) []certInfo {
	var certs []certInfo
	issuers, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	for _, issuer := range issuers {
		if !issuer.IsDir() {
			continue
		}
		hostDirs, _ := os.ReadDir(filepath.Join(dir, issuer.Name()))
		for _, hd := range hostDirs {
			if !hd.IsDir() {
				continue
			}
			crtPath := filepath.Join(dir, issuer.Name(), hd.Name(), hd.Name()+".crt")
			data, err := os.ReadFile(crtPath)
			if err != nil {
				continue
			}
			block, _ := pem.Decode(data)
			if block == nil {
				continue
			}
			cert, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				continue
			}
			host := cert.Subject.CommonName
			if len(cert.DNSNames) > 0 {
				host = strings.Join(cert.DNSNames, ", ")
			}
			daysLeft := int(time.Until(cert.NotAfter).Hours() / 24)
			certs = append(certs, certInfo{
				Host:     host,
				Expiry:   cert.NotAfter,
				DaysLeft: daysLeft,
				Issuer:   cert.Issuer.CommonName,
			})
		}
	}
	sort.Slice(certs, func(i, j int) bool { return certs[i].Host < certs[j].Host })
	return certs
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost && r.URL.Path == "/delete" {
		cidr := r.FormValue("ip")
		if cidr == "" {
			http.Error(w, "missing ip parameter", http.StatusBadRequest)
			return
		}
		if err := h.caddy.RemoveAllowedRange(cidr); err != nil {
			h.log.Error("admin: delete IP failed", "cidr", cidr, "err", err)
			http.Error(w, "failed to delete IP", http.StatusInternalServerError)
			return
		}
		h.log.Info("admin: deleted IP", "cidr", cidr)
		h.saveAllowlist()
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

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
			fmt.Fprintf(&ipItems, `<li><code>%s</code><form method="POST" action="/delete" style="display:inline"><input type="hidden" name="ip" value="%s"><button type="submit" class="del">&#x2715;</button></form></li>`, r, r)
		}
	}

	services, err := h.caddy.GetManagedServices()
	if err != nil {
		h.log.Warn("admin: could not fetch managed services", "err", err)
	}

	var hostItems strings.Builder
	if len(services) == 0 {
		hostItems.WriteString("<li>No services registered yet.</li>")
	} else {
		for _, svc := range services {
			badge, badgeClass := "docker", "badge-docker"
			if caddy.IsIPUpstream(svc.Upstream) {
				badge, badgeClass = "static", "badge-static"
			}
			fmt.Fprintf(&hostItems,
				`<li><a href="https://%s" target="_blank">%s</a><span class="badge %s">%s</span><span class="upstream">→ %s</span></li>`,
				svc.Host, svc.Host, badgeClass, badge, svc.Upstream)
		}
	}

	certs := loadCerts(h.certStorageDir)
	var certRows strings.Builder
	if len(certs) == 0 {
		certRows.WriteString(`<tr><td colspan="3" class="empty">No certificates yet.</td></tr>`)
	} else {
		for _, c := range certs {
			expiryClass := "ok"
			if c.DaysLeft < 10 {
				expiryClass = "exp-critical"
			} else if c.DaysLeft < 30 {
				expiryClass = "exp-warn"
			}
			fmt.Fprintf(&certRows,
				`<tr><td class="mono">%s</td><td class="mono %s">%s (%dd)</td><td class="issuer">%s</td></tr>`,
				c.Host, expiryClass, c.Expiry.UTC().Format("2006-01-02"), c.DaysLeft, c.Issuer)
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, page, qrDataURI, h.enrollURL, ipItems.String(), hostItems.String(), certRows.String())
}

const page = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>CaddyGate Admin</title>
<style>
  body { font-family: system-ui, sans-serif; max-width: 640px; margin: 60px auto; padding: 0 24px; color: #1a1a1a; }
  h1 { font-size: 1.6rem; font-weight: 600; margin-bottom: 0; }
  h2 { font-size: 1.1rem; font-weight: 500; margin-top: 2rem; }
  img { display: block; margin: 16px 0; border: 1px solid #e0e0e0; border-radius: 8px; padding: 8px; background: #fff; }
  .url { font-family: monospace; font-size: 0.85rem; background: #f5f5f5; padding: 10px 14px; border-radius: 6px; word-break: break-all; }
  ul { padding-left: 1.2rem; }
  li { font-family: monospace; font-size: 0.9rem; margin: 4px 0; }
  code { background: #f5f5f5; padding: 1px 5px; border-radius: 3px; }
  .label { font-size: 0.75rem; color: #888; text-transform: uppercase; letter-spacing: 0.05em; margin-bottom: 6px; }
  .del { margin-left: 8px; border: none; background: none; color: #c00; cursor: pointer; font-size: 0.9rem; padding: 0 4px; border-radius: 3px; }
  .del:hover { background: #fdd; }
  a { color: #0066cc; text-decoration: none; }
  a:hover { text-decoration: underline; }
  .badge { font-size: 0.65rem; font-family: system-ui, sans-serif; padding: 1px 6px; border-radius: 10px; margin-left: 8px; vertical-align: middle; }
  .badge-docker { background: #dbeafe; color: #1e40af; }
  .badge-static { background: #dcfce7; color: #166534; }
  .upstream { color: #888; font-size: 0.8rem; margin-left: 8px; }
  table { width: 100%%; border-collapse: collapse; font-size: 0.88rem; margin-top: 6px; }
  th { text-align: left; font-size: 0.75rem; color: #888; text-transform: uppercase; letter-spacing: 0.04em; padding: 4px 8px 4px 0; border-bottom: 1px solid #e0e0e0; }
  td { padding: 5px 8px 5px 0; border-bottom: 1px solid #f0f0f0; vertical-align: top; }
  .mono { font-family: monospace; }
  .issuer { color: #888; font-size: 0.8rem; }
  .empty { color: #888; font-style: italic; }
  .ok { color: #166534; }
  .exp-warn { color: #92400e; }
  .exp-critical { color: #991b1b; font-weight: 600; }
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

  <h2>Managed Services</h2>
  <ul>%s</ul>

  <h2>TLS Certificates</h2>
  <table>
    <thead><tr><th>Domain</th><th>Expires</th><th>Issuer</th></tr></thead>
    <tbody>%s</tbody>
  </table>
</body>
</html>`
