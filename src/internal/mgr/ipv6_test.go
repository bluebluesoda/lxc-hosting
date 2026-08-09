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
// supported prefix length (/64 down to /48). The 48-bit username hash only
// touches the low 48 bits, which are host bits for every prefix <= /64.
func TestIPv6AddrWithinSubnet(t *testing.T) {
	for _, sub := range []string{"2602:fada:6::/48", "2602:fada:6::/56", "2602:fada:6::/60", "2602:fada:6::/64"} {
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
