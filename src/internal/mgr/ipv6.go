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
//   lxdbr0 is configured with the GLOBAL /64 prefix (ipv6.routing + stateful
//   DHCPv6). Each container gets a DETERMINISTIC global address derived from
//   its username (sha256 → 48 bits), so the address is stable across
//   reinstalls and never needs to be stored or queried. The host then:
//     - deletes the duplicate /64 route on lxdbr0 (it conflicts with eth0's
//       kernel route when the host itself is inside the prefix)
//     - adds an exact /128 route per container via lxdbr0
//     - adds a proxy_ndp entry per container on the external interface, so
//       upstream neighbor solicitations for container addresses are answered
//       by the host (verified: external clients reach container addresses).
//
// No NAT, no nftables changes, no DB schema changes: the IPv6 address is
// computed from (prefix, username) on the fly.

// ipv6Suffix returns the 48-bit host part of a container's IPv6, derived
// deterministically from its username (sha256 truncated to 48 bits).
func ipv6Suffix(name string) string {
	h := sha256.Sum256([]byte(name))
	v := binary.BigEndian.Uint32(h[:4]) & 0xffff_ffff
	lo := binary.BigEndian.Uint16(h[4:6])
	// Format as three 16-bit hextets: xx:xxxx:xxxx (48 bits total).
	return fmt.Sprintf("%x:%04x:%04x", v>>16, v&0xffff, lo)
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
	// host part = prefix (first 64 bits) + :: + 48-bit username hash
	base := n.IP.To16()
	suffix := ipv6Suffix(name)
	host := net.ParseIP("::" + suffix).To16()
	addr := make(net.IP, 16)
	copy(addr[:8], base[:8])
	copy(addr[8:], host[8:])
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
	// The bridge gateway is the first usable address of the prefix (net+1).
	// The prefix length on the bridge must match the configured prefix (e.g.
	// /48 or /60), not always /64 — otherwise LXD's dnsmasq would only
	// advertise a /64 slice of a /48.
	ones, _ := n.Mask.Size()
	gwIP := n.IP.To16()
	gwIP[15]++ // last octet: ::0 -> ::1
	gw := gwIP.String()
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
