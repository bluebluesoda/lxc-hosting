package mgr

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"os/exec"
	"strconv"
	"strings"
)

// IPv6 pass-through support (verified empirically):
//
//   lxdbr0 is configured with the GLOBAL prefix — /64 or shorter (e.g. /56),
//   or the /80 slice a provider hands the host — with ipv6.routing + stateful
//   DHCPv6. Each container gets a DETERMINISTIC global address derived from
//   its username (sha256 → 32 bits, last block fixed to 0001), so the address
//   is stable across reinstalls and never needs to be stored or queried. The
//   host then:
//     - deletes the duplicate prefix route on lxdbr0 (it conflicts with
//       eth0's kernel route when the host itself is inside the prefix)
//     - adds an exact /128 route per container via lxdbr0
//     - adds a proxy_ndp entry per container on the external interface, so
//       upstream neighbor solicitations for container addresses are answered
//       by the host (verified: external clients reach container addresses).
//
// No NAT, no nftables changes, no DB schema changes: the IPv6 address is
// computed from (prefix, username) on the fly.

// ipv6Suffix returns the 48-bit host part of a container's IPv6: a 32-bit
// username hash followed by a fixed 0001 last block (a /64 address reads
// prefix:0:<32random>:1). Kept for tests/diagnostics; IPv6Addr writes the same
// bits directly into the address instead of going through the string form.
func ipv6Suffix(name string) string {
	h := sha256.Sum256([]byte(name))
	v := binary.BigEndian.Uint32(h[:4])
	return fmt.Sprintf("%x:%04x:1", v>>16, v&0xffff)
}

// IPv6Addr computes the deterministic global IPv6 address of a container from
// the configured prefix + username. Never queries LXD and never stores it.
func (m *Manager) IPv6Addr(name string) (string, error) {
	if !m.cfg.IPv6Enabled() {
		return "", nil
	}
	n, err := m.cfg.IPv6Network()
	if err != nil {
		return "", err
	}
	// Copy the FULL network address, then set the low 48 host bits to
	// [32-bit username hash][0001]. The fixed 0001 last block keeps every
	// container address off the all-zero subnet-router anycast and reads
	// nicely (for a /64: prefix:0:<32random>:1). Works for any prefix <= /80;
	// for a /80 slice it fills exactly the 48 host bits.
	addr := make(net.IP, 16)
	copy(addr, n.IP.To16())
	h := sha256.Sum256([]byte(name))
	copy(addr[10:], h[:4])
	addr[14], addr[15] = 0, 1
	return addr.String(), nil
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
	// Remove the conflicting /64 route LXD auto-creates on the bridge when the
	// host itself is inside the prefix (eth0 keeps the authoritative /64).
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
// A container's hash-derived address is 2^-32 unlikely to collide with any of
// these (and its 0001 last block can never be the all-zero anycast), and LXD
// only uses this address as the dnsmasq/SLAAC anchor.
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

// proxyNDP adds or removes a proxy_ndp entry for one address on the external
// interface, and ensures kernel proxy_ndp is enabled. Idempotent.
func (m *Manager) proxyNDP(addr string, add bool) error {
	if !m.cfg.IPv6Enabled() {
		return nil
	}
	ext := m.cfg.Net.ExtIF
	if ext == "" {
		return fmt.Errorf("no external interface for proxy_ndp")
	}
	if add {
		_ = exec.Command("sysctl", "-w", "net.ipv6.conf."+ext+".proxy_ndp=1").Run()
		// `ip -6 neigh add proxy` is idempotent-ish; ignore "already exists".
		if out, err := exec.Command("ip", "-6", "neigh", "add", "proxy", addr, "dev", ext).CombinedOutput(); err != nil &&
			!strings.Contains(string(out), "File exists") {
			return fmt.Errorf("proxy_ndp add %s: %s", addr, strings.TrimSpace(string(out)))
		}
	} else {
		_ = exec.Command("ip", "-6", "neigh", "del", "proxy", addr, "dev", ext).Run()
	}
	return nil
}

// syncRoute adds or removes the exact /128 route for one container via the
// bridge. Idempotent.
func (m *Manager) syncRoute(addr string, add bool) error {
	if !m.cfg.IPv6Enabled() || addr == "" {
		return nil
	}
	bridge := m.cfg.LXD.Bridge
	if add {
		if out, err := exec.Command("ip", "-6", "route", "add", addr+"/128", "dev", bridge).CombinedOutput(); err != nil &&
			!strings.Contains(string(out), "File exists") {
			return fmt.Errorf("route add %s: %s", addr, strings.TrimSpace(string(out)))
		}
	} else {
		_ = exec.Command("ip", "-6", "route", "del", addr+"/128", "dev", bridge).Run()
	}
	return nil
}

// WireIPv6 attaches the global IPv6 plumbing for one container: /128 route +
// proxy_ndp entry. The address is computed from the username (no waiting).
func (m *Manager) WireIPv6(name string) error {
	if !m.cfg.IPv6Enabled() {
		return nil
	}
	addr, err := m.IPv6Addr(name)
	if err != nil {
		return err
	}
	return m.wireAddr(name, addr)
}

// wireAddr applies the /128 route + proxy_ndp entry for an address.
func (m *Manager) wireAddr(name, addr string) error {
	if err := m.syncRoute(addr, true); err != nil {
		return err
	}
	return m.proxyNDP(addr, true)
}

// UnwireIPv6 removes the /128 route and proxy_ndp entry for a container.
func (m *Manager) UnwireIPv6(name string) {
	if !m.cfg.IPv6Enabled() {
		return
	}
	addr, err := m.IPv6Addr(name)
	if err != nil || addr == "" {
		return
	}
	_ = m.syncRoute(addr, false)
	_ = m.proxyNDP(addr, false)
}

// RewireAllIPv6 re-attaches /128 routes + proxy_ndp entries for every existing
// container. Called at boot (after LXD is up) and by `vpsmgr install` so that
// pass-through survives reboots. Idempotent. A container that exists in the DB
// but not in LXD (e.g. half-removed state) is skipped, not fatal — otherwise
// one stale row would break re-wiring for every other container.
func (m *Manager) RewireAllIPv6() error {
	if !m.cfg.IPv6Enabled() {
		return nil
	}
	if err := m.SetupIPv6Bridge(); err != nil {
		return err
	}
	users, err := m.db.ListUsers()
	if err != nil {
		return err
	}
	var firstErr error
	for _, u := range users {
		if err := m.WireIPv6(u.Name); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
