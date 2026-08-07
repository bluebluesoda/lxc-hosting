package fw

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"vpsmgr/internal/cfg"
)

type Firewall struct {
	cfg *cfg.Config
}

func New(c *cfg.Config) *Firewall { return &Firewall{cfg: c} }

func (f *Firewall) userFile(name string) string {
	return filepath.Join(f.cfg.NftDir(), "user-"+name+".nft")
}

func (f *Firewall) MainPath() string { return f.cfg.NftMain() }

// WriteMain writes the authoritative main config (table, chains, masquerade,
// include of per-user files).
func (f *Firewall) WriteMain() error {
	sub := f.cfg.Net.Subnet
	ext := f.cfg.Net.ExtIF
	content := fmt.Sprintf(`table inet vpsmgr {
  chain prerouting {
    type nat hook prerouting priority dstnat; policy accept;
  }

  chain output {
    type nat hook output priority dstnat; policy accept;
  }

  chain postrouting {
    type nat hook postrouting priority srcnat; policy accept;
    ip saddr %s oifname "%s" masquerade
  }
}
include "%s"
`, sub, ext, filepath.Join(f.cfg.NftDir(), "*.nft"))
	return os.WriteFile(f.MainPath(), []byte(content), 0o644)
}

// WriteUser writes the DNAT rules for a user. Two sets: prerouting (external
// traffic) and output (connections originating on the host itself, e.g. the
// acceptance test `ssh -p <base> root@<hostIP>`). Output rules are scoped to
// the host's own IP so unrelated local connections are not hijacked.
func (f *Firewall) WriteUser(name, ip string, portBase int) error {
	last := portBase + f.cfg.Net.PortsPerUser - 1
	daddr := f.cfg.Panel.PublicIP
	if daddr == "" || daddr == "127.0.0.1" {
		daddr = ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "add rule inet vpsmgr prerouting tcp dport %d dnat ip to %s:22\n", portBase, ip)
	fmt.Fprintf(&b, "add rule inet vpsmgr prerouting tcp dport %d-%d dnat ip to %s\n", portBase+1, last, ip)
	fmt.Fprintf(&b, "add rule inet vpsmgr prerouting udp dport %d-%d dnat ip to %s\n", portBase, last, ip)
	if daddr != "" {
		fmt.Fprintf(&b, "add rule inet vpsmgr output ip daddr %s tcp dport %d dnat ip to %s:22\n", daddr, portBase, ip)
		fmt.Fprintf(&b, "add rule inet vpsmgr output ip daddr %s tcp dport %d-%d dnat ip to %s\n", daddr, portBase+1, last, ip)
		fmt.Fprintf(&b, "add rule inet vpsmgr output ip daddr %s udp dport %d-%d dnat ip to %s\n", daddr, portBase, last, ip)
	}
	return os.WriteFile(f.userFile(name), []byte(b.String()), 0o600)
}

func (f *Firewall) RemoveUser(name string) error {
	return os.Remove(f.userFile(name))
}

// Reload rebuilds the vpsmgr table from the main config (delete then apply).
func (f *Firewall) Reload() error {
	del := exec.Command("nft", "delete", "table", "inet", "vpsmgr")
	if err := del.Run(); err != nil {
		// table may not exist yet; that is fine
	}
	apply := exec.Command("nft", "-f", f.MainPath())
	if out, err := apply.CombinedOutput(); err != nil {
		return fmt.Errorf("nft -f: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
