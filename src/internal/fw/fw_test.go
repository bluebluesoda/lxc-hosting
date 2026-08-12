package fw

import (
	"strings"
	"testing"

	"vpsmgr/internal/cfg"
)

// TestMainContentStartsWithDeleteTable verifies the atomic-reload contract:
// the generated config begins with `delete table`, so a single `nft -f`
// applies the whole ruleset as one batch instead of deleting and re-adding in
// two separate commands.
func TestMainContentStartsWithDeleteTable(t *testing.T) {
	c := cfg.Default()
	content := mainContent(c)
	if !strings.HasPrefix(content, "delete table inet vpsmgr\n") {
		t.Fatalf("mainContent does not start with delete table:\n%s", content)
	}
	if !strings.Contains(content, `include "/etc/vpsmgr/nftables.d/*.nft"`) {
		t.Fatalf("mainContent missing the per-user include:\n%s", content)
	}
}
