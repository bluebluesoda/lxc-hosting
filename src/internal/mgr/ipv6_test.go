package mgr

import (
	"net"
	"testing"

	"vpsmgr/internal/cfg"
)

// sha256("alice")[:4] == 2b d8 06 c9 — the 32-bit block index used below.

func TestIPv6Block(t *testing.T) {
	cases := []struct {
		subnet, want string
	}{
		// /64 parent: bits 64-79 padded with 0 -> "::" before the hash.
		{"2602:fada:6::/64", "2602:fada:6::2bd8:6c9:0/112"},
		// /80 parent: the 753a hextet is part of the prefix, hash follows.
		{"2406:da14:1dd2:a807:753a::/80", "2406:da14:1dd2:a807:753a:2bd8:6c9:0/112"},
	}
	for _, c := range cases {
		cfg := cfg.Default()
		cfg.Net.IPv6Subnet = c.subnet
		m := &Manager{cfg: cfg}
		b, err := m.IPv6Block("alice")
		if err != nil {
			t.Fatalf("%s: %v", c.subnet, err)
		}
		if got := b.String(); got != c.want {
			t.Errorf("IPv6Block(%s) = %s, want %s", c.subnet, got, c.want)
		}
	}
}

func TestIPv6Addr(t *testing.T) {
	cfg := cfg.Default()
	cfg.Net.IPv6Subnet = "2406:da14:1dd2:a807:753a::/80"
	m := &Manager{cfg: cfg}
	addr, err := m.IPv6Addr("alice")
	if err != nil {
		t.Fatal(err)
	}
	want := "2406:da14:1dd2:a807:753a:2bd8:6c9:1"
	if addr != want {
		t.Errorf("IPv6Addr(alice) = %q, want %q", addr, want)
	}
}

// A computed block must always fall inside the configured subnet, for every
// supported prefix length (/64 down to /48, plus /80 provider slices). The
// 32-bit hash only touches bits 80-111, which are host bits for any prefix
// <= /80.
func TestIPv6BlockWithinSubnet(t *testing.T) {
	for _, sub := range []string{"2602:fada:6::/48", "2602:fada:6::/56", "2602:fada:6::/60", "2602:fada:6::/64", "2406:da14:1dd2:a807:753a::/80"} {
		c := cfg.Default()
		c.Net.IPv6Subnet = sub
		m := &Manager{cfg: c}
		b, err := m.IPv6Block("alice")
		if err != nil {
			t.Fatalf("%s: %v", sub, err)
		}
		_, ipnet, err := net.ParseCIDR(sub)
		if err != nil {
			t.Fatal(err)
		}
		if !ipnet.Contains(b.IP) {
			t.Errorf("%s: block %s not inside subnet", sub, b)
		}
	}
}

// The bridge gateway (net+1, net+2, ...) must stay inside the prefix, for both
// /64 and /80 provider slices — this is the arithmetic behind avoiding a host
// or router that already holds ::1.
func TestAddHostOffset(t *testing.T) {
	cases := []struct{ subnet, want1, want2 string }{
		{"2602:fada:6::/64", "2602:fada:6::1", "2602:fada:6::2"},
		{"2406:da14:1dd2:a807:753a::/80", "2406:da14:1dd2:a807:753a::1", "2406:da14:1dd2:a807:753a::2"},
	}
	for _, c := range cases {
		_, n, err := net.ParseCIDR(c.subnet)
		if err != nil {
			t.Fatal(err)
		}
		got1 := addHostOffset(n.IP, 1).String()
		got2 := addHostOffset(n.IP, 2).String()
		if got1 != c.want1 || got2 != c.want2 {
			t.Errorf("%s: net+1=%q (want %q), net+2=%q (want %q)", c.subnet, got1, c.want1, got2, c.want2)
		}
	}
}
