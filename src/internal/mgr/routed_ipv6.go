package mgr

import (
	"fmt"
	"strings"
)

// EnsureRoutedIPv6 makes containers route to each other's public IPv6 through
// the host instead of trying direct L2 neighbour discovery (which port
// isolation blocks). The LXD bridge advertises the parent prefix as on-link,
// so a container would resolve a peer directly and the frame would die at the
// isolated bridge port. Telling systemd-networkd to ignore the RA on-link and
// route prefixes (plus dropping the ICMPv6 redirects the host would otherwise
// send to put the peer back on-link) makes every peer packet go to the
// container's default gateway — the host — which forwards it via the peer's
// /112 route. L2 between containers stays isolated: no broadcast/NDP plane, no
// MITM, only address-addressed routed traffic. No-op when IPv6 is disabled.
func (m *Manager) EnsureRoutedIPv6() error {
	if !m.cfg.IPv6Enabled() {
		return nil
	}
	n, err := m.cfg.IPv6Network()
	if err != nil {
		return err
	}
	// Bare prefix (without the trailing ::) used to match routes that belong to
	// the parent prefix, e.g. 2406:da14:1dd2:a807:753a for .../80.
	prefix := strings.TrimSuffix(n.IP.String(), "::")
	script := fmt.Sprintf(`set -e
CFG=/etc/systemd/network/eth0.network
if ! grep -qs 'UseOnLinkPrefix=false' "$CFG" 2>/dev/null; then
  printf '\n[IPv6AcceptRA]\nUseOnLinkPrefix=false\nUseRoutePrefix=false\n' >> "$CFG"
  systemctl restart systemd-networkd || true
fi
# Drop stale on-link and redirect routes for the parent prefix so this
# container stops trying to reach peers directly.
for r in $(ip -6 route show dev eth0 | awk '{print $1}'); do
  case "$r" in
    %s*) ip -6 route del "$r" dev eth0 2>/dev/null || true ;;
  esac
done
ip -6 route flush cache || true`, prefix)
	users, err := m.db.ListUsers()
	if err != nil {
		return err
	}
	var firstErr error
	for _, u := range users {
		if _, err := m.lx.ExecSH(u.Name, script); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}
