package mgr

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"net"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
)

// IPv6 pass-through support (verified empirically):
//
//   lxdbr0 is configured with the GLOBAL prefix — /64 or shorter (e.g. /56),
//   or the /80 slice a provider hands the host — with ipv6.routing + stateful
//   DHCPv6. For a shorter prefix (/48 /56 /60) the bridge carries the FIRST
//   /64 slice of it, because LXD's dnsmasq rejects non-/64 networks and every
//   deterministic container address falls in that /64 anyway.
//
//   Each container owns a DETERMINISTIC /112 block derived from its username
//   (sha256 → 32-bit block index at bits 80-111 + 16 host bits), so the block
//   is stable across reinstalls and never stored or queried. The block's
//   primary address (block + ::1) is byte-identical to the address of the old
//   single-/128 scheme, so upgrading never changes an existing container's
//   address.
//
//   Per container:
//     - the eth0 device sets ipv6.address=<block>::1 (primary) and
//       ipv6.routes=<block>::/112, so LXD routes the whole block to the
//       container and any address it binds is delivered to it.
//     - ndppd proxies Neighbor Discovery on the EXTERNAL interface for every
//       /112: an upstream neighbor solicitation for an address in a block is
//       relayed to the bridge, the container answers, ndppd relays the NA
//       back. Kernel proxy_ndp only answers single addresses (verified: it
//       ignores prefix- or route-covered queries), which is why ndppd is used.
//
//   vpsmgr renders /etc/ndppd.conf (one rule per container) and restarts the
//   daemon on add/del/reapply; RewireAllIPv6 rebuilds it from the DB at boot
//   and on `vpsmgr install`, so rules survive reboots. No NAT, no nftables
//   changes, no DB schema changes.

// ipv6Suffix returns the low 48 host bits of a container's primary IPv6 (the
// 32-bit username hash followed by a fixed 0001 last block). Kept for
// tests/diagnostics; IPv6Addr writes the same bits directly into the address.
func ipv6Suffix(name string) string {
	h := sha256.Sum256([]byte(name))
	v := binary.BigEndian.Uint32(h[:4])
	return fmt.Sprintf("%x:%04x:1", v>>16, v&0xffff)
}

// blockBits is the length of the routed prefix each container owns.
const blockBits = 112

// IPv6Block computes the deterministic /112 block a container owns, derived
// from the configured prefix + username. Never queries LXD and never stores
// it. The block index (32-bit username hash) lands at bits 80-111 for every
// supported prefix <= /80; the trailing 16 bits are the container's host
// space.
func (m *Manager) IPv6Block(name string) (*net.IPNet, error) {
	if !m.cfg.IPv6Enabled() {
		return nil, nil
	}
	n, err := m.cfg.IPv6Network()
	if err != nil {
		return nil, err
	}
	block := make(net.IP, 16)
	copy(block, n.IP.To16())
	h := sha256.Sum256([]byte(name))
	copy(block[10:14], h[:4])
	return &net.IPNet{IP: block, Mask: net.CIDRMask(blockBits, 128)}, nil
}

// IPv6Addr returns the container's primary global address — its /112 block
// plus ::1 — used as the eth0 DHCPv6 reservation. Byte-identical to the
// pre-/112 scheme, so existing container addresses never change.
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
	// interface (common with a /80 slice where the host holds ::1).
	//
	// Bridge prefix length: LXD's dnsmasq only serves /64 networks (a shorter
	// prefix like /48 /56 /60 makes it error "only /64 allowed"). Since every
	// deterministic container address lives in the FIRST /64 of the configured
	// prefix (bits [prefixlen:79] are zero-filled), we clamp the bridge to /64
	// for those — containers still fall inside it. /64 and /80 use their own
	// length.
	ones, _ := n.Mask.Size()
	bridgeOnes := bridgePrefixLen(ones)
	gw, err := m.bridgeGateway(n)
	if err != nil {
		return err
	}
	bridge := m.cfg.LXD.Bridge
	for _, kv := range []string{
		"ipv6.address=" + gw + "/" + strconv.Itoa(bridgeOnes),
		"ipv6.nat=false",
		"ipv6.routing=true",
		"ipv6.dhcp.stateful=true",
	} {
		if err := m.lx.NetworkSet(bridge, kv); err != nil {
			return err
		}
	}
	// Remove the conflicting route LXD auto-creates on the bridge for the
	// bridge's own prefix when the host itself is inside it (eth0 keeps the
	// authoritative route).
	bridgeNet := &net.IPNet{IP: net.ParseIP(gw).Mask(net.CIDRMask(bridgeOnes, 128)), Mask: net.CIDRMask(bridgeOnes, 128)}
	_ = exec.Command("ip", "-6", "route", "del", bridgeNet.String(), "dev", bridge).Run()
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

// bridgePrefixLen clamps the configured prefix length for the lxdbr0 bridge.
// LXD's dnsmasq only serves /64 networks (a /48 /56 /60 makes it error "only
// /64 allowed"), and every deterministic container address falls inside the
// FIRST /64 of the configured prefix — so shorter prefixes ride on that /64;
// /64 and /80 keep their own length.
func bridgePrefixLen(ones int) int {
	if ones < 64 {
		return 64
	}
	return ones
}

// enableForwarding turns on IPv6 forwarding (required for pass-through).
func (m *Manager) enableForwarding() error {
	out, err := exec.Command("sysctl", "-w", "net.ipv6.conf.all.forwarding=1").CombinedOutput()
	if err != nil {
		return fmt.Errorf("enable ipv6 forwarding: %s", strings.TrimSpace(string(out)))
	}
	return nil
}

// ndppdConfPath is where vpsmgr renders the ndppd rules — inside /etc/vpsmgr
// so it is covered by the service-user ownership. The pidfile lives under
// /run/vpsmgr, created by the panel / boot units via RuntimeDirectory.
const (
	ndppdConfPath = "/etc/vpsmgr/ndppd.conf"
	ndppdRunDir   = "/run/vpsmgr"
	ndppdPidPath  = "/run/vpsmgr/ndppd.pid"
)

// ndppdConf renders /etc/ndppd.conf: one `rule <block>::/112` per container
// under a `proxy <ext_if>` section, so upstream neighbor solicitations for any
// address in a container's block are relayed to the LXD bridge (the container
// answers for the addresses it binds). `add` / `drop` let a single user be
// added or removed without racing the DB transaction in Add/Del. Empty when
// IPv6 is disabled or no container has a block.
func (m *Manager) ndppdConf(add, drop string) (string, error) {
	if !m.cfg.IPv6Enabled() {
		return "", nil
	}
	ext := m.cfg.Net.ExtIF
	if ext == "" {
		return "", fmt.Errorf("no external interface for ndppd")
	}
	names := map[string]bool{}
	users, err := m.db.ListUsers()
	if err != nil {
		return "", err
	}
	for _, u := range users {
		names[u.Name] = true
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
	fmt.Fprintf(&b, "proxy %s {\n", ext)
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

// restartNDPPD (re)starts the ndppd daemon after a config write. ndppd 0.2.x
// has no live reload (SIGHUP terminates it), so a restart is required. The
// daemon always runs as the unprivileged vpsmgr user: from the panel it
// inherits the service's ambient capabilities, and from root (CLI/install) it
// is re-spawned via setpriv, which re-grants the network capabilities across
// the uid drop (ambient caps do not survive a uid change).
func (m *Manager) restartNDPPD() error {
	if _, err := exec.LookPath("ndppd"); err != nil {
		return fmt.Errorf("ndppd is not installed (install.sh installs it when IPv6 is enabled)")
	}
	if err := os.MkdirAll(ndppdRunDir, 0o755); err != nil {
		return err
	}
	_ = exec.Command("pkill", "-x", "ndppd").Run()
	// Give the old daemon a moment to release the raw sockets it bound.
	for i := 0; i < 20 && m.ndppdAlive(); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	_ = os.Remove(ndppdPidPath)
	if out, err := ndppdStartCmd().CombinedOutput(); err != nil {
		return fmt.Errorf("start ndppd: %s", strings.TrimSpace(string(out)))
	}
	if !m.ndppdAlive() {
		return fmt.Errorf("ndppd did not start")
	}
	return nil
}

// ndppdStartCmd builds the ndppd invocation. When running as root the daemon
// is launched via setpriv so it lands on the vpsmgr user with CAP_NET_ADMIN +
// CAP_NET_RAW in its ambient set (needed to bind the raw NDP sockets); the
// panel already carries those as ambient capabilities and spawns directly.
func ndppdStartCmd() *exec.Cmd {
	if os.Geteuid() == 0 {
		return exec.Command("setpriv",
			"--reuid=vpsmgr", "--regid=vpsmgr", "--init-groups",
			"--bounding-set=+net_admin,+net_raw",
			"--inh-caps=+net_admin,+net_raw",
			"--ambient-caps=+net_admin,+net_raw",
			"ndppd", "-d", "-p", ndppdPidPath)
	}
	return exec.Command("ndppd", "-d", "-p", ndppdPidPath)
}

// ndppdAlive reports whether an ndppd process is running.
func (m *Manager) ndppdAlive() bool {
	out, err := exec.Command("pgrep", "-x", "ndppd").Output()
	return err == nil && len(out) > 0
}

// writeNDPPD renders the config for the current container set (plus/minus one
// container) and restarts the daemon. When no container has IPv6 routing the
// daemon is stopped, so a stale config can never misroute.
func (m *Manager) writeNDPPD(add, drop string) error {
	conf, err := m.ndppdConf(add, drop)
	if err != nil {
		return err
	}
	if conf == "" {
		_ = exec.Command("pkill", "-x", "ndppd").Run()
		_ = os.Remove(ndppdPidPath)
		_ = os.Remove(ndppdConfPath)
		return nil
	}
	if err := os.WriteFile(ndppdConfPath, []byte(conf), 0o644); err != nil {
		return err
	}
	return m.restartNDPPD()
}

// WireIPv6 registers a container's /112 with the NDP proxy so its addresses
// are reachable from the internet. The block is computed from the username (no
// waiting); the LXD device already routes the /112 to the container.
func (m *Manager) WireIPv6(name string) error {
	if !m.cfg.IPv6Enabled() {
		return nil
	}
	return m.writeNDPPD(name, "")
}

// UnwireIPv6 removes a container's /112 from the NDP proxy.
func (m *Manager) UnwireIPv6(name string) {
	if !m.cfg.IPv6Enabled() {
		return
	}
	_ = m.writeNDPPD("", name)
}

// cleanLegacyKernelProxy removes the per-address kernel proxy_ndp entries and
// /128 routes the old (pre-/112) scheme installed on the external interface
// and the bridge. Idempotent, best-effort: the /112 routes programmed by LXD
// supersede both. Only touches addresses inside the configured prefix.
func (m *Manager) cleanLegacyKernelProxy() {
	if !m.cfg.IPv6Enabled() {
		return
	}
	ext := m.cfg.Net.ExtIF
	bridge := m.cfg.LXD.Bridge
	users, err := m.db.ListUsers()
	if err != nil {
		return
	}
	for _, u := range users {
		addr, err := m.IPv6Addr(u.Name)
		if err != nil || addr == "" {
			continue
		}
		if ext != "" {
			_ = exec.Command("ip", "-6", "neigh", "del", "proxy", addr, "dev", ext).Run()
		}
		_ = exec.Command("ip", "-6", "route", "del", addr+"/128", "dev", bridge).Run()
	}
}

// RewireAllIPv6 rebuilds the whole IPv6 pass-through: bridge config, the
// ndppd rules for every container, and a sweep of the old kernel per-address
// plumbing. Called at boot (after LXD is up) and by `vpsmgr install` so that
// pass-through survives reboots. Idempotent.
func (m *Manager) RewireAllIPv6() error {
	if !m.cfg.IPv6Enabled() {
		return nil
	}
	if err := m.SetupIPv6Bridge(); err != nil {
		return err
	}
	m.cleanLegacyKernelProxy()
	return m.writeNDPPD("", "")
}
