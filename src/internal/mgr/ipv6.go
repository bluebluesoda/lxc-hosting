package mgr

import (
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
)

// IPv6 pass-through support (verified empirically):
//
//   lxdbr0 is configured with the GLOBAL prefix — /64 or shorter (e.g. /56),
//   or the /80 slice a provider hands the host — with ipv6.routing + stateful
//   DHCPv6. Each container gets its own DETERMINISTIC /112 block derived from
//   its username (sha256 → 32-bit block index), so the block is stable across
//   reinstalls and never needs to be stored or queried:
//
//       block = <prefix>< 32-bit sha256(name) >::/112
//                 bits 0-79           bits 80-111     bits 112-127 (host)
//
//   For a /64 parent the bits 64-79 are zero (the network address already
//   zeroes all host bits), i.e. the "::" padding — a /80 parent keeps them as
//   part of the prefix. Either way the 32-bit hash always lands at bits 80-111
//   and the container owns the trailing 16 host bits.
//
//   Per container:
//     - LXD routes the whole /112 to the container: the eth0 device override
//       sets ipv6.address=<block>::1 (a DHCPv6 reservation, the container's
//       primary address) and ipv6.routes=<block>::/112 (LXD programs the
//       route on lxdbr0). Any address in the /112 the container binds is
//       therefore delivered to it.
//     - The kernel's proxy_ndp only works per single address, so a /112 can't
//       be ND-proxied with `ip neigh add proxy` (verified: route-covered
//       addresses are NOT answered). Instead ndppd proxies the /112 at the
//       upstream: a Neighbor Solicitation on the external interface for an
//       address in a container's /112 is relayed to lxdbr0, the container
//       answers for the addresses it binds, and ndppd relays the NA back.
//
//   No NAT, no nftables changes, no DB schema changes: the /112 block is
//   computed from (prefix, username) on the fly.

// blockBits is the length of the per-container routed prefix.
const blockBits = 112

// IPv6Block computes the deterministic /112 block a container owns, derived
// from the configured prefix + username. Never queries LXD, never stores it.
func (m *Manager) IPv6Block(name string) (*net.IPNet, error) {
	if !m.cfg.IPv6Enabled() {
		return nil, nil
	}
	n, err := m.cfg.IPv6Network()
	if err != nil {
		return nil, err
	}
	// Copy the FULL network address (host bits are zero), then write the
	// 32-bit username hash at bytes 10-13 (bits 80-111). Bytes 14-15 stay
	// zero: they are the /112's 16 host bits.
	block := make(net.IP, 16)
	copy(block, n.IP.To16())
	h := sha256.Sum256([]byte(name))
	copy(block[10:14], h[:4])
	return &net.IPNet{IP: block, Mask: net.CIDRMask(blockBits, 128)}, nil
}

// IPv6Addr returns the container's primary global address inside its /112
// (block + ::1), used as the LXD eth0 DHCPv6 reservation.
func (m *Manager) IPv6Addr(name string) (string, error) {
	b, err := m.IPv6Block(name)
	if err != nil || b == nil {
		return "", err
	}
	return addHostOffset(b.IP, 1).String(), nil
}

// SetupIPv6Bridge configures lxdbr0 for IPv6 pass-through. Idempotent.
func (m *Manager) SetupIPv6Bridge() error {
	if !m.cfg.IPv6Enabled() {
		return nil
	}
	n, err := m.cfg.IPv6Network()
	if err != nil {
		return err
	}
	// The bridge gateway is a free address inside the prefix — normally net+1,
	// but skipped when the host itself already uses it on the external
	// interface (common with a /80 slice where the host holds ::1). The prefix
	// length on the bridge must match the configured prefix (e.g. /48, /60 or
	// /80), not always /64 — otherwise LXD's dnsmasq would only advertise a
	// /64 slice of a bigger prefix.
	ones, _ := n.Mask.Size()
	gw, err := m.bridgeGateway(n)
	if err != nil {
		return err
	}
	bridge := m.cfg.LXD.Bridge
	for _, kv := range []string{
		"ipv6.address=" + gw + "/" + strconv.Itoa(ones),
		"ipv6.nat=false",
		"ipv6.routing=true",
		"ipv6.dhcp.stateful=true",
	} {
		if err := m.lx.NetworkSet(bridge, kv); err != nil {
			return err
		}
	}
	// Remove the conflicting prefix route LXD auto-creates on the bridge when
	// the host itself is inside the prefix (eth0 keeps the authoritative one).
	_ = exec.Command("ip", "-6", "route", "del", n.String(), "dev", bridge).Run()
	_ = m.enableForwarding()
	return nil
}

// bridgeGateway picks the first usable address inside the prefix (net+1,
// net+2, ...) that is not already taken by anything the host can see:
//   - addresses assigned to the host's external interface (e.g. a /80 slice
//     where the host itself holds ::1)
//   - the upstream default gateway(s) — a global gateway inside the prefix
//     (very common with a /64, where the ISP's router is at ::1) must never be
//     claimed by the bridge, or the host would answer for it and break its own
//     outbound routing
//   - any address present in the NDP neighbor table on the external interface
//     (catches the router and any other device already on the link)
//
// A container's hash-derived block is 2^-32 unlikely to collide with any of
// these, and LXD only uses this address as the dnsmasq/SLAAC anchor.
func (m *Manager) bridgeGateway(n *net.IPNet) (string, error) {
	inUse := map[string]bool{}
	ext := m.cfg.Net.ExtIF

	// 1. Addresses the host itself holds on the external interface.
	if ext != "" {
		out, err := exec.Command("ip", "-6", "-o", "addr", "show", "dev", ext, "scope", "global").CombinedOutput()
		if err != nil {
			return "", fmt.Errorf("list ipv6 addrs on %s: %s", ext, strings.TrimSpace(string(out)))
		}
		for _, f := range strings.Fields(string(out)) {
			addr := strings.SplitN(f, "/", 2)[0]
			if ip := net.ParseIP(addr); ip != nil {
				inUse[ip.String()] = true
			}
		}
	}

	// 2. Default gateway(s) — `via` addresses in `ip -6 route show default`.
	if out, err := exec.Command("ip", "-6", "route", "show", "default").CombinedOutput(); err == nil {
		for _, f := range strings.Fields(string(out)) {
			if ip := net.ParseIP(f); ip != nil {
				inUse[ip.String()] = true
			}
		}
	}

	// 3. Already-resolved neighbors on the upstream link (router, other hosts).
	if ext != "" {
		if out, err := exec.Command("ip", "-6", "neigh", "show", "dev", ext).CombinedOutput(); err == nil {
			for _, f := range strings.Fields(string(out)) {
				if ip := net.ParseIP(f); ip != nil {
					inUse[ip.String()] = true
				}
			}
		}
	}

	base := n.IP.To16()
	for k := uint64(1); k < 1<<16; k++ {
		ip := addHostOffset(base, k)
		if !inUse[ip.String()] {
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("no free gateway address in %s", n.String())
}

// addHostOffset returns the network address + k, incrementing the low 64 host
// bits big-endian with carry. k only ever touches host bits for any prefix
// <= /80 (48+ host bits), so the result stays inside the subnet.
func addHostOffset(netAddr net.IP, k uint64) net.IP {
	ip := make(net.IP, 16)
	copy(ip, netAddr)
	for i := 15; i >= 8 && k > 0; i-- {
		v := uint64(ip[i]) + (k & 0xff)
		ip[i] = byte(v & 0xff)
		k >>= 8
		if v > 0xff {
			k++
		}
	}
	return ip
}

// enableForwarding turns on IPv6 forwarding (required for pass-through).
func (m *Manager) enableForwarding() error {
	out, err := exec.Command("sysctl", "-w", "net.ipv6.conf.all.forwarding=1").CombinedOutput()
	if err != nil {
		return fmt.Errorf("enable ipv6 forwarding: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

const ndppdConfPath = "/etc/ndppd.conf"

// ndppdRules renders the ndppd config: one `rule <block>::/112` per container
// under a `proxy <ext_if>` section, so upstream neighbor solicitations for any
// address in a container's /112 are relayed to the LXD bridge (the container
// answers for the addresses it binds). `add` / `drop` let a single user be
// added or removed without racing the DB transaction in Add/Del.
func (m *Manager) ndppdRules(add, drop string) (string, error) {
	if !m.cfg.IPv6Enabled() {
		return "", nil
	}
	names := map[string]bool{}
	if users, err := m.db.ListUsers(); err != nil {
		return "", err
	} else {
		for _, u := range users {
			names[u.Name] = true
		}
	}
	if drop != "" {
		delete(names, drop)
	}
	if add != "" {
		names[add] = true
	}
	if len(names) == 0 {
		return "", nil
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	var b strings.Builder
	fmt.Fprintf(&b, "proxy %s {\n", m.cfg.Net.ExtIF)
	for _, n := range sorted {
		block, err := m.IPv6Block(n)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(&b, "   rule %s {\n      iface %s\n   }\n", block.String(), m.cfg.LXD.Bridge)
	}
	b.WriteString("}\n")
	return b.String(), nil
}

// writeNDPPD renders the config for the current container set (plus/minus one
// container) and reloads the daemon. When no container has IPv6 routing the
// daemon is stopped, so a stale config can never misroute.
func (m *Manager) writeNDPPD(add, drop string) error {
	cfg, err := m.ndppdRules(add, drop)
	if err != nil {
		return err
	}
	if cfg == "" {
		// No container has IPv6 routing; leave no stale rules behind.
		_ = exec.Command("service", "ndppd", "stop").Run()
		return nil
	}
	tmp := ndppdConfPath + ".tmp"
	if err := os.WriteFile(tmp, []byte(cfg), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, ndppdConfPath); err != nil {
		return err
	}
	return m.reloadNDPPD()
}

// reloadNDPPD applies the new rules by restarting the daemon. ndppd 0.2.4 has
// no live reload — SIGHUP terminates it (verified) — so a quick restart is the
// only way to pick up config changes. The init script owns the pidfile, so
// vpsmgr can never spawn a second instance. A restart also works when the
// daemon is not running yet (boot, before any container exists).
func (m *Manager) reloadNDPPD() error {
	if out, err := exec.Command("service", "ndppd", "restart").CombinedOutput(); err != nil {
		return fmt.Errorf("restart ndppd: %s", strings.TrimSpace(string(out)))
	}
	// The init.d script reports success even if the daemon dies right after
	// starting (e.g. bad config); verify it is actually alive.
	if _, err := exec.Command("pgrep", "-x", "ndppd").CombinedOutput(); err != nil {
		return fmt.Errorf("ndppd not running after restart")
	}
	return nil
}

// WireIPv6 registers the container's /112 with the NDP proxy (ndppd). The
// block is computed from the username, no LXD query needed.
func (m *Manager) WireIPv6(name string) error {
	if !m.cfg.IPv6Enabled() {
		return nil
	}
	return m.writeNDPPD(name, "")
}

// UnwireIPv6 removes the container's /112 rule from the NDP proxy.
func (m *Manager) UnwireIPv6(name string) {
	if !m.cfg.IPv6Enabled() {
		return
	}
	_ = m.writeNDPPD("", name)
}

// RewireAllIPv6 re-registers every existing container's /112 with the NDP
// proxy and re-applies the bridge config. Called at boot (after LXD is up) and
// by `vpsmgr install` so pass-through survives reboots. Idempotent.
func (m *Manager) RewireAllIPv6() error {
	if !m.cfg.IPv6Enabled() {
		return nil
	}
	if err := m.SetupIPv6Bridge(); err != nil {
		return err
	}
	return m.writeNDPPD("", "")
}
