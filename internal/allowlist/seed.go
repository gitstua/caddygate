package allowlist

import (
	"fmt"
	"log/slog"

	"github.com/yourorg/caddygate/internal/caddy"
)

// Seed pushes the initial set of CIDRs into Caddy's allowlist.
// Safe to call on every startup — duplicates are ignored by the Caddy client.
func Seed(client *caddy.Client, cidrs []string, log *slog.Logger) error {
	for _, cidr := range cidrs {
		if err := client.AddAllowedRange(cidr); err != nil {
			return fmt.Errorf("seeding CIDR %s: %w", cidr, err)
		}
		log.Info("allowlist: seeded CIDR", "cidr", cidr)
	}
	return nil
}
