package cfg

import "testing"

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
