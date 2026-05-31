package allowlist

import (
	"encoding/json"
	"log/slog"
	"os"

	"github.com/gitstua/caddygate/internal/caddy"
)

// Save writes the current Caddy allowlist to a JSON file.
func Save(client *caddy.Client, path string, log *slog.Logger) {
	ranges, err := client.GetAllowedRanges()
	if err != nil {
		log.Warn("allowlist persist: could not read ranges", "err", err)
		return
	}
	b, err := json.Marshal(ranges)
	if err != nil {
		log.Warn("allowlist persist: marshal failed", "err", err)
		return
	}
	if err := os.WriteFile(path, b, 0600); err != nil {
		log.Warn("allowlist persist: write failed", "path", path, "err", err)
	}
}

// Load reads a persisted allowlist file and seeds it into Caddy.
func Load(client *caddy.Client, path string, log *slog.Logger) {
	b, err := os.ReadFile(path)
	if err != nil {
		if !os.IsNotExist(err) {
			log.Warn("allowlist persist: read failed", "path", path, "err", err)
		}
		return
	}
	var ranges []string
	if err := json.Unmarshal(b, &ranges); err != nil {
		log.Warn("allowlist persist: unmarshal failed", "err", err)
		return
	}
	if len(ranges) == 0 {
		return
	}
	if err := Seed(client, ranges, log); err != nil {
		log.Warn("allowlist persist: seed failed", "err", err)
		return
	}
	log.Info("allowlist restored from disk", "count", len(ranges))
}
