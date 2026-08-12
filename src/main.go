package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"vpsmgr/internal/admin"
	"vpsmgr/internal/cert"
	"vpsmgr/internal/cfg"
	"vpsmgr/internal/db"
	"vpsmgr/internal/fw"
	"vpsmgr/internal/inter"
	"vpsmgr/internal/mgr"
	"vpsmgr/internal/panel"
	"vpsmgr/internal/pw"
	"vpsmgr/internal/ver"
)

const panelUnit = `[Unit]
Description=vpsmgr panel
After=network-online.target
Wants=network-online.target
[Service]
Type=simple
ExecStart=/usr/local/bin/vps serve
Restart=always
RestartSec=3
[Install]
WantedBy=multi-user.target
`

const nftUnit = `[Unit]
Description=vpsmgr nftables rules
After=network-online.target lxd.service
Wants=network-online.target
Before=vpsmgr-panel.service
[Service]
Type=oneshot
ExecStart=/bin/sh -c 'nft add table inet vpsmgr 2>/dev/null; exec nft -f /etc/vpsmgr/nftables.conf'
RemainAfterExit=yes
[Install]
WantedBy=multi-user.target
`

const ipv6Unit = `[Unit]
Description=vpsmgr IPv6 pass-through routes
After=network-online.target lxd.service vpsmgr-nft.service
Wants=network-online.target
Before=vpsmgr-panel.service
[Service]
Type=oneshot
ExecStart=/usr/local/bin/vps ipv6-reapply
RemainAfterExit=yes
[Install]
WantedBy=multi-user.target
`

func main() {
	log.SetFlags(0)
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "install":
		err = cmdInstall()
	case "serve":
		err = cmdServe()
	case "panel-url":
		err = cmdPanelURL()
	case "add":
		err = userAdd(os.Args[2:])
	case "del":
		if len(os.Args) != 3 {
			err = fmt.Errorf("usage: vps del <name>")
			break
		}
		err = userDel(os.Args[2])
	case "list":
		err = userList()
	case "update":
		err = userUpdate(os.Args[2:])
	case "start", "stop", "restart":
		if len(os.Args) != 3 {
			err = fmt.Errorf("usage: vps %s <name>", os.Args[1])
			break
		}
		err = userPower(os.Args[1], os.Args[2])
	case "reset-passwd":
		if len(os.Args) != 3 {
			err = fmt.Errorf("usage: vps reset-passwd <name>")
			break
		}
		err = userResetPasswd(os.Args[2])
	case "admin-passwd":
		err = cmdAdminPasswd()
	case "v4-forward":
		if len(os.Args) != 3 {
			err = fmt.Errorf("usage: vps v4-forward on|off")
			break
		}
		err = cmdV4Forward(os.Args[2])
	case "show":
		if len(os.Args) != 3 {
			err = fmt.Errorf("usage: vps show <name>")
			break
		}
		err = userShow(os.Args[2])
	case "ipv6-reapply":
		// Re-attach IPv6 routes/proxy_ndp for all existing containers.
		// Run by the vpsmgr-ipv6.service boot unit and `vps install`.
		err = cmdIPv6Reapply()
	case "note-version":
		if len(os.Args) != 3 {
			err = fmt.Errorf("usage: vps note-version <version>")
			break
		}
		err = cmdNoteVersion(os.Args[2])
	case "version":
		fmt.Println(ver.Version)
		return
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`vps ` + ver.Version + `
usage:
  vps add <name> [--cpu 1] [--mem 1G] [--disk 10G] [--traffic 100]
  vps update <name> [--cpu 2] [--mem 2G] [--disk 20G] [--traffic 200]
  vps reset-passwd <name>    # reissue panel password (shown once)
  vps admin-passwd           # reset admin panel password (shown once)
  vps start|stop|restart <name>
  vps del <name>
  vps list
  vps show <name>
  vps panel-url              print panel address
  vps v4-forward on|off      enable/disable IPv4 inbound (ssh/ports/domains); rules refresh
  vps note-version <ver>     record binary version that left this config (used by uninstall.sh)
  vps version
cpu:  whole cores >= 1 (e.g. --cpu 2), or a fraction of one core in 0.1..0.9
      (e.g. --cpu 0.5 — the container is pinned to one core with a time slice)
traffic: monthly quota in GiB (upload + download combined); 0 or empty = unlimited
`)
}

func openDB() (*db.DB, error) {
	c, err := cfg.Load()
	if err != nil {
		return nil, fmt.Errorf("load config: %w", err)
	}
	return db.Open(c.Panel.DB)
}

// panelPath returns the secret panel path (e.g. "/Ab1_cdE-9x") or "" if none.
func panelPath(c *cfg.Config) string {
	if c == nil || c.Panel.URLPath == "" {
		return ""
	}
	return "/" + c.Panel.URLPath
}

// blockUnadoptable refuses to run against a config/db that records an origin
// older than this binary can adopt. v0.3 makes breaking changes, so a config
// whose origin is 0.2.x (or older, or not recorded) must not be adopted:
// `vps install` would migrate it, and `vps serve` runs the db migration
// on open — either would corrupt 0.2.x data. Guard both entry points.
//
// The origin is installed_version when set (the last binary to adopt the
// config), falling back to uninstalled_version (a kept config from a
// non-purging uninstall). Only when NEITHER is set is the origin unknown and
// treated as too old. A config that already records 0.3.x must not be blocked
// on an empty counterpart field — that is a normal 0.3.x re-upgrade.
func blockUnadoptable(c *cfg.Config) error {
	origin := c.InstalledVersion
	if origin == "" {
		origin = c.UninstalledVersion
	}
	if ver.Blocked(origin) {
		return fmt.Errorf("vpsmgr %s cannot adopt a setup from an older release "+
			"(config origin %s): v0.3 makes breaking changes and the migration "+
			"from 0.2.x is not ready yet. Do not upgrade — stay on v0.2.x until "+
			"a migration path exists.",
			ver.Version, orUnknown(origin))
	}
	return nil
}

func orUnknown(v string) string {
	if v == "" {
		return "(unknown)"
	}
	return v
}

func cmdInstall() error {
	c := cfg.Default()
	adopting := false
	if _, err := os.Stat(cfg.Path()); err == nil {
		c, err = cfg.Load()
		if err != nil {
			return err
		}
		adopting = true
	} else {
		if err := c.FillAuto(); err != nil {
			return err
		}
		// Fresh install: generate both secret paths (user 10 / admin 12).
		// After this, an empty path is a deliberate "panel disabled" choice.
		c.EnsurePaths()
		// Fresh install: pick a random free panel port in 2000-9999 instead of
		// the fixed 8443. Best-effort — if every random pick is busy (very
		// unlikely on a fresh host) the code-level default is kept.
		if p, err := cfg.RandomPanelPort(); err == nil {
			c.Panel.Listen = fmt.Sprintf(":%d", p)
		}
	}
	// Adopting an existing config: refuse to touch one that came from a release
	// too old to migrate. Check BEFORE overwriting installed_version below.
	if adopting {
		if err := blockUnadoptable(c); err != nil {
			return err
		}
	}
	// Record which binary version installed (or adopted/upgraded) this config so
	// a future release that makes breaking changes can detect the version the
	// config/db came from and migrate or warn instead of corrupting user data.
	c.InstalledVersion = ver.Version
	if err := cfg.Save(c); err != nil {
		return err
	}
	if err := c.ValidatePaths(); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DefaultDataDir, 0o755); err != nil {
		return err
	}
	// LXD's security.ipv6_filtering (enabled on every container's eth0) only
	// works while the br_netfilter kernel module is loaded, and LXD does NOT
	// load it itself: a container with the option simply refuses to boot
	// without it. Load it BEFORE any container is created/hardened, and persist
	// it in /etc/modules-load.d so it is present at boot, before LXD starts any
	// container. Harmless no-op where the module is built into the kernel.
	if err := os.MkdirAll("/etc/modules-load.d", 0o755); err != nil {
		return err
	}
	if err := os.WriteFile("/etc/modules-load.d/br_netfilter.conf", []byte("br_netfilter\n"), 0o644); err != nil {
		return err
	}
	_ = exec.Command("modprobe", "br_netfilter").Run()
	if err := os.MkdirAll(cfg.DefaultNftDir, 0o755); err != nil {
		return err
	}
	if err := cert.Ensure(c.Panel.Cert, c.Panel.Key, c.Panel.PublicIP); err != nil {
		return err
	}
	if _, err := os.Stat(c.Panel.DB); err != nil {
		d, err := db.Open(c.Panel.DB)
		if err != nil {
			return err
		}
		d.Close()
	}
	// IPv6 pass-through: configure bridge + re-attach routes for existing
	// containers (no-op when ipv6_subnet empty).
	d, err := db.Open(c.Panel.DB)
	if err != nil {
		return err
	}
	m := mgr.New(c, d)
	if err := m.RewireAllIPv6(); err != nil {
		d.Close()
		return fmt.Errorf("setup ipv6: %w", err)
	}
	// Container isolation: harden any containers created before the isolated
	// build so the whole fleet is on the same security posture.
	if err := m.HardenAll(); err != nil {
		d.Close()
		return fmt.Errorf("harden containers: %w", err)
	}
	// /112 blocks: add ipv6.routes to containers created before the /112
	// scheme, so each container's whole block is routed to it.
	if err := m.EnsureBlockRoutes(); err != nil {
		d.Close()
		return fmt.Errorf("add ipv6 routes: %w", err)
	}
	// Repair containers sharing a baked-in machine-id (shared DHCPv6 DUID
	// breaks lease renewals and drops the global IPv6 at the 1h mark).
	if err := m.EnsureUniqueMachineID(); err != nil {
		d.Close()
		return fmt.Errorf("unique machine-id: %w", err)
	}
	// Route inter-container IPv6 through the host (no L2 discovery / MITM),
	// so a container can reach a peer whose address it knows.
	if err := m.EnsureRoutedIPv6(); err != nil {
		d.Close()
		return fmt.Errorf("routed ipv6: %w", err)
	}
	d.Close()
	f := fw.New(c)
	if err := f.WriteMain(); err != nil {
		return err
	}
	if err := f.Reload(); err != nil {
		return err
	}
	if err := writeUnit("vpsmgr-panel.service", panelUnit); err != nil {
		return err
	}
	if err := writeUnit("vpsmgr-nft.service", nftUnit); err != nil {
		return err
	}
	// IPv6 pass-through boot unit (re-applies routes/proxy after reboot).
	if c.IPv6Enabled() {
		if err := writeUnit("vpsmgr-ipv6.service", ipv6Unit); err != nil {
			return err
		}
	}
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("daemon-reload: %s", strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "enable", "--now", "vpsmgr-nft.service").CombinedOutput(); err != nil {
		return fmt.Errorf("enable vpsmgr-nft: %s", strings.TrimSpace(string(out)))
	}
	if c.IPv6Enabled() {
		if out, err := exec.Command("systemctl", "enable", "--now", "vpsmgr-ipv6.service").CombinedOutput(); err != nil {
			return fmt.Errorf("enable vpsmgr-ipv6: %s", strings.TrimSpace(string(out)))
		}
	}
	if out, err := exec.Command("systemctl", "enable", "--now", "vpsmgr-panel.service").CombinedOutput(); err != nil {
		return fmt.Errorf("enable vpsmgr-panel: %s", strings.TrimSpace(string(out)))
	}
	// Enforce the IPv4 inbound policy: per-user DNAT rules + traefik state
	// (v4_forward off = IPv6-only, traefik disabled). Idempotent.
	d2, err := db.Open(c.Panel.DB)
	if err != nil {
		return err
	}
	m2 := mgr.New(c, d2)
	if err := m2.ApplyV4State(); err != nil {
		d2.Close()
		return fmt.Errorf("apply v4 policy: %w", err)
	}
	d2.Close()
	// Admin panel: on a FRESH install (admin enabled and no password yet)
	// generate a random admin password and show it once. On adoption/upgrade
	// the existing hash is kept — never reprint a password the admin may not
	// expect to be displayed. When admin is disabled nothing is printed.
	if c.Panel.AdminPath != "" && c.Panel.AdminPass == "" {
		pass := pw.Generate(20)
		hash, err := pw.Hash(pass)
		if err != nil {
			return err
		}
		c.Panel.AdminPass = hash
		if err := cfg.Save(c); err != nil {
			return err
		}
		fmt.Printf("admin panel initialized: %s\n", c.PanelURL("/"+c.Panel.AdminPath))
		fmt.Printf("admin password (shown once): %s\n", pass)
	}
	if c.Panel.URLPath != "" {
		fmt.Printf("panel initialized: %s\n", c.PanelURL(panelPath(c)))
	}
	return nil
}

// cmdIPv6Reapply re-attaches IPv6 pass-through plumbing for all existing
// containers (bridge config, /128 routes, proxy_ndp) and re-applies the
// per-container routed-IPv6 config. No-op when IPv6 disabled.
func cmdIPv6Reapply() error {
	c, err := cfg.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	d, err := db.Open(c.Panel.DB)
	if err != nil {
		return err
	}
	defer d.Close()
	m := mgr.New(c, d)
	if err := m.RewireAllIPv6(); err != nil {
		return err
	}
	// Also re-apply the per-container routed-IPv6 config, so the boot unit and
	// a manual `vps ipv6-reapply` heal containers that were created before
	// the host-routed scheme existed or whose networkd config got corrupted.
	return m.EnsureRoutedIPv6()
}

func writeUnit(name, content string) error {
	p := filepath.Join("/etc/systemd/system", name)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func cmdServe() error {
	c, err := cfg.Load()
	if err != nil {
		return err
	}
	if err := blockUnadoptable(c); err != nil {
		return err
	}
	if err := c.ValidatePaths(); err != nil {
		return fmt.Errorf("invalid panel paths: %w", err)
	}
	// Empty path = that panel is disabled. When BOTH are empty the panel
	// service is intentionally off: do not even listen on the port.
	if c.Panel.URLPath == "" && c.Panel.AdminPath == "" {
		log.Printf("both url_path and admin_url_path are empty — panel disabled, not listening on %s", c.Panel.Listen)
		return nil
	}
	d, err := db.Open(c.Panel.DB)
	if err != nil {
		return err
	}
	defer d.Close()
	m := mgr.New(c, d)
	userPath := panelPath(c)
	adminPath := "/" + c.Panel.AdminPath
	var userSrv, adminSrv http.Handler
	if userPath != "" {
		userSrv = panel.New(c, d, m).Handler()
	}
	if c.Panel.AdminPath != "" {
		adminSrv = admin.New(c, d, m).Handler()
	}
	dispatch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case userSrv != nil && pathUnder(r.URL.Path, userPath):
			userSrv.ServeHTTP(w, r)
		case adminSrv != nil && pathUnder(r.URL.Path, adminPath):
			adminSrv.ServeHTTP(w, r)
		default:
			// No matching enabled panel: bare 404, no body, no headers, no
			// fingerprint.
			w.WriteHeader(http.StatusNotFound)
		}
	})
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	go sampleTrafficLoop(m)
	log.Printf("panel listening on %s (https, self-signed)", c.Panel.Listen)
	return startTLS(c, dispatch, tlsCfg)
}

// pathUnder reports whether path equals prefix or starts with prefix+"/".
func pathUnder(path, prefix string) bool {
	if prefix == "" || prefix == "/" {
		return false
	}
	if path == prefix {
		return true
	}
	return strings.HasPrefix(path, prefix+"/")
}

// sampleTrafficLoop runs the monthly traffic collector until the process ends.
// After every sample it enforces traffic quotas: users over their monthly
// limit get their NIC rate-limited to 1Mbps, users back under (e.g. monthly
// rollover) get the limit removed. LXD applies the NIC limits live (tc), so no
// container restart is involved.
func sampleTrafficLoop(m *mgr.Manager) {
	if err := m.SampleTraffic(); err != nil {
		log.Printf("traffic sample: %v", err)
	}
	if err := m.EnforceTrafficLimits(); err != nil {
		log.Printf("traffic limits: %v", err)
	}
	tick := time.NewTicker(mgr.TrafficInterval)
	defer tick.Stop()
	for range tick.C {
		if err := m.SampleTraffic(); err != nil {
			log.Printf("traffic sample: %v", err)
		}
		if err := m.EnforceTrafficLimits(); err != nil {
			log.Printf("traffic limits: %v", err)
		}
	}
}

func startTLS(c *cfg.Config, h http.Handler, tlsCfg *tls.Config) error {
	srv := &http.Server{Addr: c.Panel.Listen, Handler: h, TLSConfig: tlsCfg}
	return srv.ListenAndServeTLS(c.Panel.Cert, c.Panel.Key)
}

// cmdNoteVersion records the version of the binary that is being uninstalled.
// uninstall.sh calls it with the running binary's version BEFORE removing the
// binary, so a config kept by a non-purging uninstall remembers which version it
// came from. A future release that makes breaking changes can use this to warn
// or migrate instead of failing on incompatible data.
func cmdNoteVersion(v string) error {
	if v == "" {
		return fmt.Errorf("version is empty")
	}
	c, err := cfg.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	c.UninstalledVersion = v
	if err := cfg.Save(c); err != nil {
		return err
	}
	fmt.Printf("recorded version %s (last uninstalled)\n", v)
	return nil
}

func cmdPanelURL() error {
	c, err := cfg.Load()
	if err != nil {
		return err
	}
	if c.Panel.URLPath != "" {
		fmt.Printf("user panel:  %s\n", c.PanelURL(panelPath(c)))
	}
	if c.Panel.AdminPath != "" {
		fmt.Printf("admin panel: %s\n", c.PanelURL("/"+c.Panel.AdminPath))
	}
	if c.Panel.URLPath == "" && c.Panel.AdminPath == "" {
		fmt.Println("both panels are disabled (url_path and admin_url_path are empty)")
	}
	return nil
}

// cmdV4Forward toggles the shared IPv4 inbound policy. Turning it off makes
// every container IPv6-only (no SSH DNAT, no user-port-block DNAT, traefik
// disabled); turning it on restores everything from the recorded ports/domains.
func cmdV4Forward(state string) error {
	on := state == "on"
	if state != "on" && state != "off" {
		return fmt.Errorf("usage: vps v4-forward on|off")
	}
	c, err := cfg.Load()
	if err != nil {
		return err
	}
	if c.Net.V4Forward == on {
		fmt.Printf("v4 forwarding already %s\n", state)
		return nil
	}
	c.Net.V4Forward = on
	if err := cfg.Save(c); err != nil {
		return err
	}
	d, err := db.Open(c.Panel.DB)
	if err != nil {
		return err
	}
	defer d.Close()
	m := mgr.New(c, d)
	if err := m.ApplyV4State(); err != nil {
		return err
	}
	if on {
		fmt.Println("v4 forwarding enabled: ssh/port-block DNAT restored, traefik re-enabled (domains re-synced)")
	} else {
		fmt.Println("v4 forwarding disabled: ssh/port-block DNAT removed, traefik disabled (domains kept)")
	}
	return nil
}

// cmdAdminPasswd resets the admin panel password to a new random 20-char value
// and prints it once. The password is stored as a bcrypt hash in the config.
func cmdAdminPasswd() error {
	c, err := cfg.Load()
	if err != nil {
		return err
	}
	if c.Panel.AdminPath == "" {
		return fmt.Errorf("admin panel is disabled (admin_url_path is empty) — set it in %s to enable", cfg.Path())
	}
	pass := pw.Generate(20)
	hash, err := pw.Hash(pass)
	if err != nil {
		return err
	}
	c.Panel.AdminPass = hash
	if err := cfg.Save(c); err != nil {
		return err
	}
	fmt.Printf("admin password reset: %s\n", pass)
	fmt.Printf("admin panel: %s\n", c.PanelURL("/"+c.Panel.AdminPath))
	return nil
}

func userAdd(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: vps add <name> [--cpu 1] [--mem 1G] [--disk 10G]")
	}
	name := args[0]
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var cpuS string
	var memS, diskS, trafficS string
	fs.StringVar(&cpuS, "cpu", "", "")
	fs.StringVar(&memS, "mem", "", "")
	fs.StringVar(&diskS, "disk", "", "")
	fs.StringVar(&trafficS, "traffic", "", "") // GiB/month, 0/empty = unlimited
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if err := mgr.ValidateName(name); err != nil {
		return err
	}
	provided := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { provided[f.Name] = true })

	cpu, mem, disk := 10, 1024, 10 // tenths of a core, MiB, GiB
	setCpu, setMem, setDisk := provided["cpu"], provided["mem"], provided["disk"]
	var err error
	if setCpu {
		if cpu, err = mgr.ParseCPU(cpuS); err != nil {
			return err
		}
	}
	if setMem {
		if mem, err = parseMemStrict(memS); err != nil {
			return err
		}
	}
	if setDisk {
		if disk, err = parseDiskStrict(diskS); err != nil {
			return err
		}
	}

	traffic := 0
	if setTraffic := provided["traffic"]; setTraffic {
		if traffic, err = mgr.ParseTrafficGB(trafficS); err != nil {
			return err
		}
	}

	if inter.IsTTY() {
		if !setCpu {
			s, err := inter.Ask("CPU cores", "1", "", validateCPU)
			if err != nil {
				return err
			}
			cpu, _ = mgr.ParseCPU(s)
		}
		if !setMem {
			s, err := inter.Ask("Memory", "1024", " MiB (e.g. 512 or 1G)", validateMem)
			if err != nil {
				return err
			}
			mem, _ = parseMemStrict(s)
		}
		if !setDisk {
			s, err := inter.Ask("Disk", "10", " GiB", validateDisk)
			if err != nil {
				return err
			}
			disk, _ = parseDiskStrict(s)
		}
		if !provided["traffic"] {
			s, err := inter.Ask("Traffic quota", "0", " GiB/month (0 = unlimited)", validateTrafficGB)
			if err != nil {
				return err
			}
			traffic, _ = mgr.ParseTrafficGB(s)
		}
	}

	c, err := cfg.Load()
	if err != nil {
		return err
	}
	d, err := db.Open(c.Panel.DB)
	if err != nil {
		return err
	}
	defer d.Close()
	m := mgr.New(c, d)
	res, err := m.Add(name, mgr.AddOptions{CPU: cpu, MemMB: mem, DiskGB: disk, TrafficGB: traffic})
	if err != nil {
		return err
	}
	printAdded(res)
	return nil
}

func userDel(name string) error {
	c, err := cfg.Load()
	if err != nil {
		return err
	}
	d, err := db.Open(c.Panel.DB)
	if err != nil {
		return err
	}
	defer d.Close()
	m := mgr.New(c, d)
	if err := m.Del(name); err != nil {
		return err
	}
	fmt.Printf("user %s deleted (container, nft rules, traefik config, records)\n", name)
	return nil
}

func userUpdate(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: vps update <name> [--cpu 2] [--mem 2G] [--disk 20G]")
	}
	name := args[0]
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var cpuS string
	var memS, diskS, trafficS string
	fs.StringVar(&cpuS, "cpu", "", "")
	fs.StringVar(&memS, "mem", "", "")
	fs.StringVar(&diskS, "disk", "", "")
	fs.StringVar(&trafficS, "traffic", "", "") // GiB/month, 0 = unlimited
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if err := mgr.ValidateName(name); err != nil {
		return err
	}
	provided := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { provided[f.Name] = true })

	c, err := cfg.Load()
	if err != nil {
		return err
	}
	d, err := db.Open(c.Panel.DB)
	if err != nil {
		return err
	}
	defer d.Close()
	u, err := d.GetUserByName(name)
	if err != nil {
		return err
	}

	cpu, mem, disk := u.CPU, u.MemMB, u.DiskGB
	setCpu, setMem, setDisk := provided["cpu"], provided["mem"], provided["disk"]
	if setCpu {
		if cpu, err = mgr.ParseCPU(cpuS); err != nil {
			return err
		}
	}
	if setMem {
		if mem, err = parseMemStrict(memS); err != nil {
			return err
		}
	}
	if setDisk {
		if disk, err = parseDiskStrict(diskS); err != nil {
			return err
		}
	}
	setTraffic := provided["traffic"]
	trafficGB := u.TrafficQuotaGB
	if setTraffic {
		if trafficGB, err = mgr.ParseTrafficGB(trafficS); err != nil {
			return err
		}
	}

	if inter.IsTTY() {
		fmt.Printf("current quota: CPU %s / mem %d MiB / disk %d GiB / traffic %d GiB\n", mgr.FormatCPU(u.CPU), u.MemMB, u.DiskGB, u.TrafficQuotaGB)
		if !setCpu {
			s, err := inter.Ask("new CPU cores", mgr.FormatCPU(cpu), "", validateCPU)
			if err != nil {
				return err
			}
			cpu, _ = mgr.ParseCPU(s)
		}
		if !setMem {
			s, err := inter.Ask("new memory", strconv.Itoa(mem), " MiB (e.g. 512 or 1G)", validateMem)
			if err != nil {
				return err
			}
			mem, _ = parseMemStrict(s)
		}
		if !setDisk {
			s, err := inter.Ask("new disk", strconv.Itoa(disk), " GiB (only grow allowed)", validateDisk)
			if err != nil {
				return err
			}
			disk, _ = parseDiskStrict(s)
		}
		if !setTraffic {
			s, err := inter.Ask("new traffic quota", strconv.Itoa(trafficGB), " GiB/month (0 = unlimited)", validateTrafficGB)
			if err != nil {
				return err
			}
			trafficGB, _ = mgr.ParseTrafficGB(s)
		}
	}

	trafficChanged := setTraffic || trafficGB != u.TrafficQuotaGB
	if cpu == u.CPU && mem == u.MemMB && disk == u.DiskGB && !trafficChanged {
		if inter.IsTTY() {
			fmt.Println("no changes, exiting")
			return nil
		}
		return fmt.Errorf("nothing to update: pass at least one of --cpu/--mem/--disk/--traffic")
	}
	if disk < u.DiskGB {
		return fmt.Errorf("disk can only grow: current %d GiB, cannot shrink to %d GiB", u.DiskGB, disk)
	}

	m := mgr.New(c, d)
	if cpu != u.CPU || mem != u.MemMB || disk != u.DiskGB {
		if _, err := m.UpdateQuotas(name, cpu, mem, disk); err != nil {
			return err
		}
	}
	if trafficChanged {
		if err := m.SetTrafficQuota(name, trafficGB); err != nil {
			return err
		}
	}
	fresh, err := d.GetUserByName(name)
	if err != nil {
		return err
	}
	printResult(m.ResultFor(fresh, ""))
	return nil
}

// userResetPasswd resets a user's panel login password to a random 20-char
// value and prints it once. The container root password is not touched.
func userResetPasswd(name string) error {
	if err := mgr.ValidateName(name); err != nil {
		return err
	}
	c, err := cfg.Load()
	if err != nil {
		return err
	}
	d, err := db.Open(c.Panel.DB)
	if err != nil {
		return err
	}
	defer d.Close()
	m := mgr.New(c, d)
	pass, err := m.ResetPanelPassword(name)
	if err != nil {
		return err
	}
	fmt.Printf("panel password reset for %s: %s\n", name, pass)
	return nil
}

func userPower(action, name string) error {
	c, err := cfg.Load()
	if err != nil {
		return err
	}
	d, err := db.Open(c.Panel.DB)
	if err != nil {
		return err
	}
	defer d.Close()
	m := mgr.New(c, d)
	if err := m.Power(name, action); err != nil {
		return err
	}
	fmt.Printf("user %s: %s ok\n", name, action)
	return nil
}

func userList() error {
	c, err := cfg.Load()
	if err != nil {
		return err
	}
	d, err := db.Open(c.Panel.DB)
	if err != nil {
		return err
	}
	defer d.Close()
	m := mgr.New(c, d)
	results, err := m.List()
	if err != nil {
		return err
	}
	fmt.Printf("%-16s %-14s %-14s %-10s %-6s %-8s %-7s %-8s %-8s %-6s %-10s\n", "NAME", "IP", "PORTS", "STATE", "CPU", "MEM", "DISK", "UP_GB", "DOWN_GB", "CPU%", "MEMUSE")
	for _, r := range results {
		ports := mgr.UserPorts(r.User.StartPort, r.PortsPerUser)
		if !r.V4Forward {
			ports = "v4-off"
		}
		fmt.Printf("%-16s %-14s %-14s %-10s %-6s %-8d %-7d %-8s %-8s %-6s %-10s\n",
			r.User.Name, r.User.IP, ports,
			r.State, mgr.FormatCPU(r.User.CPU), r.User.MemMB, r.User.DiskGB, r.UpGB, r.DownGB, r.CPUUse, r.MemUse)
	}
	return nil
}

func userShow(name string) error {
	c, err := cfg.Load()
	if err != nil {
		return err
	}
	d, err := db.Open(c.Panel.DB)
	if err != nil {
		return err
	}
	defer d.Close()
	m := mgr.New(c, d)
	res, err := m.Show(name)
	if err != nil {
		return err
	}
	printResult(res)
	return nil
}

// printAdded prints the essentials of a freshly created user. Live stats (CPU%,
// memory, traffic, domains) are all empty on a brand-new container and only add
// noise, so they are intentionally skipped here.
func printAdded(r *mgr.Result) {
	u := r.User
	fmt.Printf("name:     %s\n", u.Name)
	fmt.Printf("state:    %s\n", r.State)
	if r.V4Forward {
		fmt.Printf("ssh:      %d\n", u.SSHPort)
		fmt.Printf("ports:    %s\n", mgr.UserPorts(u.StartPort, r.PortsPerUser))
	} else {
		fmt.Printf("ssh:      %d (v4 inbound disabled — connect over IPv6)\n", u.SSHPort)
		fmt.Printf("ports:    %s (v4 inbound disabled)\n", mgr.UserPorts(u.StartPort, r.PortsPerUser))
	}
	fmt.Printf("quotas:   %s cpu / %d MiB / %d GiB / traffic %d GiB\n", mgr.FormatCPU(u.CPU), u.MemMB, u.DiskGB, u.TrafficQuotaGB)
	if r.IPv6 != "" {
		fmt.Printf("ipv6:     %s\n", r.IPv6)
	}
	if r.Password != "" {
		fmt.Printf("password: %s  (panel + root)\n", r.Password)
		if r.V4Forward {
			fmt.Printf("ssh:      ssh -p %d root@%s\n", u.SSHPort, r.PublicIP)
		} else {
			fmt.Printf("ssh:      v4 ssh unavailable (v6-only box) — ssh root@%s\n", r.IPv6)
		}
		c, _ := cfg.Load()
		fmt.Printf("panel:    %s\n", c.PanelURL(panelPath(c)))
	}
}

func printResult(r *mgr.Result) {
	u := r.User
	fmt.Printf("name:     %s\n", u.Name)
	fmt.Printf("state:    %s\n", r.State)
	fmt.Printf("ip:       %s\n", u.IP)
	if r.IPv6 != "" {
		fmt.Printf("ipv6:     %s\n", r.IPv6)
	}
	if r.V4Forward {
		fmt.Printf("ssh:      %d\n", u.SSHPort)
		fmt.Printf("ports:    %s\n", mgr.UserPorts(u.StartPort, r.PortsPerUser))
	} else {
		fmt.Printf("ssh:      %d (v4 inbound disabled — connect over IPv6)\n", u.SSHPort)
		fmt.Printf("ports:    %s (v4 inbound disabled)\n", mgr.UserPorts(u.StartPort, r.PortsPerUser))
	}
	fmt.Printf("quotas:   %s cpu / %d MiB / %d GiB / traffic %d GiB\n", mgr.FormatCPU(u.CPU), u.MemMB, u.DiskGB, u.TrafficQuotaGB)
	fmt.Printf("cpu use:  %s\n", r.CPUUse)
	fmt.Printf("mem use:  %s\n", r.MemUse)
	fmt.Printf("traffic:  up %s GB / down %s GB (this month)\n", r.UpGB, r.DownGB)
	fmt.Printf("domains:  %s\n", strings.Join(r.Domains, ", "))
	if r.Password != "" {
		c, _ := cfg.Load()
		fmt.Printf("password: %s  (panel + root)\n", r.Password)
		if r.V4Forward {
			fmt.Printf("ssh:      ssh -p %d root@%s\n", u.SSHPort, r.PublicIP)
		} else {
			fmt.Printf("ssh:      v4 ssh unavailable (v6-only box) — ssh root@%s\n", r.IPv6)
		}
		fmt.Printf("panel:    %s\n", c.PanelURL(panelPath(c)))
	}
}

var reInt = regexp.MustCompile(`^\d+$`)

func validateCPU(s string) error {
	_, err := mgr.ParseCPU(s)
	return err
}

// parseMemStrict parses a memory size into MiB: bare integer (MiB) or integer
// with M/G suffix. Decimals are rejected.
func parseMemStrict(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty input")
	}
	mult := 1
	last := s[len(s)-1]
	switch {
	case last >= '0' && last <= '9':
	case last == 'M' || last == 'm':
		s = s[:len(s)-1]
	case last == 'G' || last == 'g':
		mult = 1024
		s = s[:len(s)-1]
	default:
		return 0, fmt.Errorf("memory must be an integer in MiB (e.g. 512) or with a suffix (e.g. 1G)")
	}
	if !reInt.MatchString(s) {
		return 0, fmt.Errorf("memory must be an integer number of MiB (e.g. 512) or a suffix (e.g. 1G), not a decimal")
	}
	n, _ := strconv.Atoi(s)
	n *= mult
	if n < 64 {
		return 0, fmt.Errorf("memory must be at least 64 MiB")
	}
	return n, nil
}

func validateMem(s string) error {
	_, err := parseMemStrict(s)
	return err
}

// parseDiskStrict parses a disk size into GiB: bare integer or G suffix.
func parseDiskStrict(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty input")
	}
	if last := s[len(s)-1]; last == 'G' || last == 'g' {
		s = s[:len(s)-1]
	}
	if !reInt.MatchString(s) {
		return 0, fmt.Errorf("disk must be an integer number of GiB (e.g. 10 or 10G), not a decimal")
	}
	n, _ := strconv.Atoi(s)
	if n < 1 {
		return 0, fmt.Errorf("disk must be at least 1 GiB")
	}
	return n, nil
}

func validateDisk(s string) error {
	_, err := parseDiskStrict(s)
	return err
}

func validateTrafficGB(s string) error {
	_, err := mgr.ParseTrafficGB(s)
	return err
}
