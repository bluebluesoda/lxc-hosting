package mgr

import (
	"net"
	"testing"

	"vpsmgr/internal/cfg"
)

func TestIPv6Suffix(t *testing.T) {
	want := "2bd8:06c9:7f0e"
	if got := ipv6Suffix("alice"); got != want {
		t.Errorf("ipv6Suffix(alice) = %q, want %q", got, want)
	}
}

func TestIPv6Addr(t *testing.T) {
	c := cfg.Default()
	c.Net.IPv6Subnet = "2602:fada:6::/64"
	m := &Manager{cfg: c}
	addr, err := m.IPv6Addr("alice")
	if err != nil {
		t.Fatal(err)
	}
	want := "2602:fada:6::2bd8:6c9:7f0e"
	if addr != want {
		t.Errorf("IPv6Addr(alice) = %q, want %q", addr, want)
	}
}

// A computed address must always fall inside the configured subnet, for any
// supported prefix length (/64 down to /48, plus /80 provider slices). The
// 48-bit username hash only touches the low 48 bits, which are host bits for
// every prefix <= /80.
func TestIPv6AddrWithinSubnet(t *testing.T) {
	for _, sub := range []string{"2602:fada:6::/48", "2602:fada:6::/56", "2602:fada:6::/60", "2602:fada:6::/64", "2406:da14:1dd2:a807:753a::/80"} {
		c := cfg.Default()
		c.Net.IPv6Subnet = sub
		m := &Manager{cfg: c}
		addr, err := m.IPv6Addr("alice")
		if err != nil {
			t.Fatalf("%s: %v", sub, err)
		}
		_, ipnet, err := net.ParseCIDR(sub)
		if err != nil {
			t.Fatal(err)
		}
		if !ipnet.Contains(net.ParseIP(addr)) {
			t.Errorf("%s: addr %s not inside subnet", sub, addr)
		}
	}
}

// A /80 provider slice must keep ALL prefix bits (e.g. the 753a hextet) — only
// the low 48 host bits may come from the username hash.
func TestIPv6Addr80(t *testing.T) {
	c := cfg.Default()
	c.Net.IPv6Subnet = "2406:da14:1dd2:a807:753a::/80"
	m := &Manager{cfg: c}
	addr, err := m.IPv6Addr("alice")
	if err != nil {
		t.Fatal(err)
	}
	want := "2406:da14:1dd2:a807:753a:2bd8:6c9:7f0e"
	if addr != want {
		t.Errorf("IPv6Addr(alice) = %q, want %q", addr, want)
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
