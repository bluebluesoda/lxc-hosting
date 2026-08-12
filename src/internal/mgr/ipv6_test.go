package mgr

import (
	"net"
	"path/filepath"
	"strings"
	"testing"

	"vpsmgr/internal/cfg"
	"vpsmgr/internal/db"
)

func TestIPv6Suffix(t *testing.T) {
	want := "2bd8:06c9:1"
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
	want := "2602:fada:6::2bd8:6c9:1"
	if addr != want {
		t.Errorf("IPv6Addr(alice) = %q, want %q", addr, want)
	}
}

// A computed address must always fall inside the configured subnet, for any
// supported prefix length (/64 down to /48, plus /80 provider slices). The
// 32-bit username hash + fixed 0001 block only touch the low 48 bits, which
// are host bits for every prefix <= /80.
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
// the low 48 host bits may come from the username hash + the fixed 0001 block.
func TestIPv6Addr80(t *testing.T) {
	c := cfg.Default()
	c.Net.IPv6Subnet = "2406:da14:1dd2:a807:753a::/80"
	m := &Manager{cfg: c}
	addr, err := m.IPv6Addr("alice")
	if err != nil {
		t.Fatal(err)
	}
	want := "2406:da14:1dd2:a807:753a:2bd8:6c9:1"
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

// The bridge is always >= /64: LXD's dnsmasq rejects non-/64 networks, and
// all deterministic container addresses live in the first /64 of the prefix.
func TestBridgePrefixLen(t *testing.T) {
	cases := []struct{ ones, want int }{
		{48, 64}, {56, 64}, {60, 64}, {64, 64}, {80, 80},
	}
	for _, c := range cases {
		if got := bridgePrefixLen(c.ones); got != c.want {
			t.Errorf("bridgePrefixLen(%d) = %d, want %d", c.ones, got, c.want)
		}
	}
}

// The primary address is byte-identical across the old single-/128 scheme and
// the /112 block scheme: the 32-bit username hash at bits 80-111 plus a fixed
// 0001 host block. This is what lets the upgrade keep every existing container
// address.
func TestIPv6Block(t *testing.T) {
	c := cfg.Default()
	c.Net.IPv6Subnet = "2602:fada:6::/64"
	m := &Manager{cfg: c}
	b, err := m.IPv6Block("alice")
	if err != nil {
		t.Fatal(err)
	}
	if b == nil {
		t.Fatal("nil block")
	}
	ones, _ := b.Mask.Size()
	if ones != 112 {
		t.Errorf("block mask = %d, want 112", ones)
	}
	// Host bits are zero — the block is a network address, not a host one.
	if b.IP.To16()[14] != 0 || b.IP.To16()[15] != 0 {
		t.Errorf("block host bits not zero: %s", b.IP)
	}
	// primary = block + 1, which is exactly what IPv6Addr reports.
	if got := addHostOffset(b.IP, 1).String(); got != "2602:fada:6::2bd8:6c9:1" {
		t.Errorf("block+1 = %s, want 2602:fada:6::2bd8:6c9:1", got)
	}
	// The block must live inside the configured subnet.
	_, ipnet, err := net.ParseCIDR(c.Net.IPv6Subnet)
	if err != nil {
		t.Fatal(err)
	}
	if !ipnet.Contains(b.IP) {
		t.Errorf("block %s outside subnet", b.String())
	}
	// Two distinct names must not share a block.
	if b2, _ := m.IPv6Block("bob"); b2 != nil && b2.IP.String() == b.IP.String() {
		t.Errorf("alice and bob share block %s", b)
	}
}

// checkIPv6BlockCollision must refuse a new container whose deterministic
// block is already taken by another user, skip the user itself, and be a no-op
// for a nil block (IPv6 disabled).
func TestCheckIPv6BlockCollision(t *testing.T) {
	c := cfg.Default()
	c.Net.IPv6Subnet = "2602:fada:6::/64"
	d, err := db.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.CreateUser("alice", "x", "10.42.0.2", 1, 10000, 1, 1024, 10); err != nil {
		t.Fatal(err)
	}
	m := &Manager{cfg: c, db: d}
	aliceBlock, err := m.IPv6Block("alice")
	if err != nil {
		t.Fatal(err)
	}
	// A different name claiming alice's block must be refused.
	if err := m.checkIPv6BlockCollision("bob", aliceBlock); err == nil || !strings.Contains(err.Error(), "alice") {
		t.Errorf("expected collision error naming alice, got %v", err)
	}
	// Re-adding the same name is always fine (self is skipped).
	if err := m.checkIPv6BlockCollision("alice", aliceBlock); err != nil {
		t.Errorf("self should be skipped: %v", err)
	}
	// A fresh name with its own block passes.
	bobBlock, _ := m.IPv6Block("bob")
	if err := m.checkIPv6BlockCollision("bob", bobBlock); err != nil {
		t.Errorf("fresh block should pass: %v", err)
	}
	// nil block (IPv6 disabled) -> no-op.
	if err := m.checkIPv6BlockCollision("bob", nil); err != nil {
		t.Errorf("nil block should be a no-op: %v", err)
	}
}
