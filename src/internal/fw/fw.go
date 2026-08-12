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

// mainContent renders the authoritative main config: delete the table first so
// `nft -f` applies the whole ruleset as ONE atomic batch (if any rule fails,
// the previous table survives instead of vanishing mid-reload).
func mainContent(c *cfg.Config) string {
	sub := c.Net.Subnet
	ext := c.Net.ExtIF
	return fmt.Sprintf(`delete table inet vpsmgr
table inet vpsmgr {
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
`, sub, ext, filepath.Join(c.NftDir(), "*.nft"))
}

// WriteMain writes the authoritative main config (table, chains, masquerade,
// include of per-user files).
func (f *Firewall) WriteMain() error {
	return os.WriteFile(f.MainPath(), []byte(mainContent(f.cfg)), 0o644)
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

// Reload rebuilds the vpsmgr table as one atomic nft batch. nft rules do not
// survive a reboot, so on boot the table does not exist and a `delete table`
// inside the batch would fail it; `nft add table` is idempotent, so ensure the
// table exists first. The batch then delete-and-recreates atomically: any rule
// error rolls the whole batch back and the previous table stays intact.
func (f *Firewall) Reload() error {
	_ = exec.Command("nft", "add", "table", "inet", "vpsmgr").Run()
	apply := exec.Command("nft", "-f", f.MainPath())
	if out, err := apply.CombinedOutput(); err != nil {
		return fmt.Errorf("nft -f: %s", strings.TrimSpace(string(out)))
	}
	return nil
}
