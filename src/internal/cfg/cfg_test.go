package cfg

import (
	"strings"
	"testing"
)

func TestIPv6Network(t *testing.T) {
	cases := []struct {
		subnet string
		want   string // canonical CIDR; "" = must be rejected
	}{
		{"2602:fada:6::/64", "2602:fada:6::/64"},
		{"2406:da14:1dd2:a807:753a::/80", "2406:da14:1dd2:a807:753a::/80"},
		{"2001:db8::/32", "2001:db8::/32"},
		// A bare address must be rejected, not silently assumed /64 — a /80
		// slice would then get addresses outside the routed prefix.
		{"2602:fada:6::", ""},
		{"2406:da14:1dd2:a807:753a::", ""},
		// Non-global prefixes and /96 (too few host bits) must be rejected.
		{"fe80::/64", ""},
		{"2406:da14:1dd2:a807:753a::/96", ""},
	}
	for _, c := range cases {
		cfg := Default()
		cfg.Net.IPv6Subnet = c.subnet
		n, err := cfg.IPv6Network()
		if c.want == "" {
			if err == nil {
				t.Errorf("IPv6Network(%q): expected error, got %s", c.subnet, n)
			}
			continue
		}
		if err != nil {
			t.Errorf("IPv6Network(%q): unexpected error: %v", c.subnet, err)
			continue
		}
		if got := n.String(); got != c.want {
			t.Errorf("IPv6Network(%q) = %s, want %s", c.subnet, got, c.want)
		}
	}
}

func TestValidatePaths(t *testing.T) {
	good := Default()
	good.Panel.URLPath = "UserSecRet99"
	good.Panel.AdminPath = "Adm1n-SecretX"
	if err := good.ValidatePaths(); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	// Empty path = panel disabled: a single enabled panel or none at all is OK.
	onlyUser := Default()
	onlyUser.Panel.URLPath = "UserSecRet99"
	onlyUser.Panel.AdminPath = ""
	if err := onlyUser.ValidatePaths(); err != nil {
		t.Fatalf("user-only config rejected: %v", err)
	}
	onlyAdmin := Default()
	onlyAdmin.Panel.URLPath = ""
	onlyAdmin.Panel.AdminPath = "Adm1n-SecretX"
	if err := onlyAdmin.ValidatePaths(); err != nil {
		t.Fatalf("admin-only config rejected: %v", err)
	}
	bothOff := Default()
	bothOff.Panel.URLPath = ""
	bothOff.Panel.AdminPath = ""
	if err := bothOff.ValidatePaths(); err != nil {
		t.Fatalf("both-disabled config rejected: %v", err)
	}
	// Rejections: too short (enabled path) or the two paths colliding.
	cases := []struct {
		name      string
		user, adm string
	}{
		{"short user", "short", "Adm1n-SecretX"},
		{"short admin", "UserSecRet99", "short"},
		{"equal", "SameSecret", "SameSecret"},
	}
	for _, c := range cases {
		cfg := Default()
		cfg.Panel.URLPath = c.user
		cfg.Panel.AdminPath = c.adm
		if err := cfg.ValidatePaths(); err == nil {
			t.Errorf("ValidatePaths(%q,%q): expected error", c.user, c.adm)
		}
	}
}

func TestEnsurePaths(t *testing.T) {
	// Both empty -> fresh install: both paths generated (user 10, admin 12).
	cfg := Default()
	cfg.EnsurePaths()
	if len(cfg.Panel.URLPath) != 10 {
		t.Errorf("url_path len = %d, want 10", len(cfg.Panel.URLPath))
	}
	if len(cfg.Panel.AdminPath) != 12 {
		t.Errorf("admin_url_path len = %d, want 12", len(cfg.Panel.AdminPath))
	}
	if cfg.Panel.URLPath == cfg.Panel.AdminPath {
		t.Fatal("generated paths must differ")
	}
	for _, s := range []string{cfg.Panel.URLPath, cfg.Panel.AdminPath} {
		for _, r := range s {
			if !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-_", r) {
				t.Fatalf("path %q contains invalid char %q", s, r)
			}
		}
	}
	// One side empty is a deliberate disable: EnsurePaths must NOT touch it.
	userOnly := Default()
	userOnly.Panel.URLPath = "UserSecRet99"
	userOnly.EnsurePaths()
	if userOnly.Panel.URLPath != "UserSecRet99" || userOnly.Panel.AdminPath != "" {
		t.Fatalf("EnsurePaths changed a deliberate user-only config: %q / %q", userOnly.Panel.URLPath, userOnly.Panel.AdminPath)
	}
	adminOnly := Default()
	adminOnly.Panel.AdminPath = "Adm1n-SecretX"
	adminOnly.EnsurePaths()
	if adminOnly.Panel.URLPath != "" || adminOnly.Panel.AdminPath != "Adm1n-SecretX" {
		t.Fatalf("EnsurePaths changed a deliberate admin-only config: %q / %q", adminOnly.Panel.URLPath, adminOnly.Panel.AdminPath)
	}
}
