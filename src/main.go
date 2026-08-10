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
ExecStart=/usr/local/bin/vpsmgr serve
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
ExecStart=/bin/sh -c 'nft delete table inet vpsmgr 2>/dev/null; exec nft -f /etc/vpsmgr/nftables.conf'
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
ExecStart=/usr/local/bin/vpsmgr ipv6-reapply
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
			err = fmt.Errorf("usage: vpsmgr del <name>")
			break
		}
		err = userDel(os.Args[2])
	case "list":
		err = userList()
	case "update":
		err = userUpdate(os.Args[2:])
	case "start", "stop", "restart":
		if len(os.Args) != 3 {
			err = fmt.Errorf("usage: vpsmgr %s <name>", os.Args[1])
			break
		}
		err = userPower(os.Args[1], os.Args[2])
	case "reset-passwd":
		if len(os.Args) != 3 {
			err = fmt.Errorf("usage: vpsmgr reset-passwd <name>")
			break
		}
		err = userResetPasswd(os.Args[2])
	case "admin-passwd":
		err = cmdAdminPasswd()
	case "show":
		if len(os.Args) != 3 {
			err = fmt.Errorf("usage: vpsmgr show <name>")
			break
		}
		err = userShow(os.Args[2])
	case "ipv6-reapply":
		// Re-attach IPv6 routes/proxy_ndp for all existing containers.
		// Run by the vpsmgr-ipv6.service boot unit and `vpsmgr install`.
		err = cmdIPv6Reapply()
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
	fmt.Print(`vpsmgr ` + ver.Version + `
usage:
  vpsmgr add <name> [--password X] [--cpu 1] [--mem 1G] [--disk 10G]
  vpsmgr update <name> [--cpu 2] [--mem 2G] [--disk 20G]
  vpsmgr reset-passwd <name>    # reissue panel password (shown once)
  vpsmgr admin-passwd           # reset admin panel password (shown once)
  vpsmgr start|stop|restart <name>
  vpsmgr del <name>
  vpsmgr list
  vpsmgr show <name>
  vpsmgr panel-url              print panel address
  vpsmgr version
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

func cmdInstall() error {
	c := cfg.Default()
	if _, err := os.Stat(cfg.Path()); err == nil {
		// Load() fills missing admin_url_path via FillAuto; persist it when the
		// on-disk config predates the admin panel feature so the generated
		// secret survives the restart.
		raw, _ := os.ReadFile(cfg.Path())
		hadAdminPath := strings.Contains(string(raw), "admin_url_path")
		c, err = cfg.Load()
		if err != nil {
			return err
		}
		if !hadAdminPath {
			if err := cfg.Save(c); err != nil {
				return err
			}
		}
	} else {
		if err := c.FillAuto(); err != nil {
			return err
		}
		if err := cfg.Save(c); err != nil {
			return err
		}
	}
	if err := c.ValidatePaths(); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.DefaultDataDir, 0o755); err != nil {
		return err
	}
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
	// Admin panel: on a FRESH install generate a random admin password and show
	// it once. On adoption/upgrade the existing hash is kept — never reprint a
	// password the admin may not expect to be displayed.
	fresh := c.Panel.AdminPass == ""
	if fresh {
		pass := pw.Generate(20)
		hash, err := pw.Hash(pass)
		if err != nil {
			return err
		}
		c.Panel.AdminPass = hash
		if err := cfg.Save(c); err != nil {
			return err
		}
		fmt.Printf("admin panel initialized: https://%s:8443/%s\n", c.DisplayIP(), c.Panel.AdminPath)
		fmt.Printf("admin password (shown once): %s\n", pass)
	}
	fmt.Printf("panel initialized: https://%s:8443%s\n", c.DisplayIP(), panelPath(c))
	return nil
}

// cmdIPv6Reapply re-attaches IPv6 pass-through plumbing for all existing
// containers (bridge config, /128 routes, proxy_ndp). No-op when IPv6 disabled.
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
	return m.RewireAllIPv6()
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
	// cfg.Load() auto-fills missing secret paths exactly like install does
	// (user 10 chars, admin 12 chars), so an empty path is repaired, not a
	// startup failure. ValidatePaths still guards the real hazards: paths too
	// short (after fill) or the two paths colliding.
	c, err := cfg.Load()
	if err != nil {
		return err
	}
	if err := c.ValidatePaths(); err != nil {
		return fmt.Errorf("invalid panel paths: %w", err)
	}
	d, err := db.Open(c.Panel.DB)
	if err != nil {
		return err
	}
	defer d.Close()
	m := mgr.New(c, d)
	userSrv := panel.New(c, d, m)
	adminSrv := admin.New(c, d, m)
	userPath := panelPath(c)
	adminPath := "/" + c.Panel.AdminPath
	dispatch := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case pathUnder(r.URL.Path, userPath):
			userSrv.Handler().ServeHTTP(w, r)
		case pathUnder(r.URL.Path, adminPath):
			adminSrv.Handler().ServeHTTP(w, r)
		default:
			// Neither secret path: bare 404, no body, no headers, no
			// fingerprint — identical to the user panel's behavior.
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
func sampleTrafficLoop(m *mgr.Manager) {
	if err := m.SampleTraffic(); err != nil {
		log.Printf("traffic sample: %v", err)
	}
	tick := time.NewTicker(mgr.TrafficInterval)
	defer tick.Stop()
	for range tick.C {
		if err := m.SampleTraffic(); err != nil {
			log.Printf("traffic sample: %v", err)
		}
	}
}

func startTLS(c *cfg.Config, h http.Handler, tlsCfg *tls.Config) error {
	srv := &http.Server{Addr: c.Panel.Listen, Handler: h, TLSConfig: tlsCfg}
	return srv.ListenAndServeTLS(c.Panel.Cert, c.Panel.Key)
}

func cmdPanelURL() error {
	c, err := cfg.Load()
	if err != nil {
		return err
	}
	fmt.Printf("user panel:  https://%s:8443%s\n", c.DisplayIP(), panelPath(c))
	fmt.Printf("admin panel: https://%s:8443/%s\n", c.DisplayIP(), c.Panel.AdminPath)
	return nil
}

// cmdAdminPasswd resets the admin panel password to a new random 20-char value
// and prints it once. The password is stored as a bcrypt hash in the config.
func cmdAdminPasswd() error {
	c, err := cfg.Load()
	if err != nil {
		return err
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
	fmt.Printf("admin panel: https://%s:8443/%s\n", c.DisplayIP(), c.Panel.AdminPath)
	return nil
}

func userAdd(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: vpsmgr add <name> [--password X] [--cpu 1] [--mem 1G] [--disk 10G]")
	}
	name := args[0]
	fs := flag.NewFlagSet("add", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var cpuFlag int
	var memS, diskS, passS string
	fs.StringVar(&passS, "password", "", "")
	fs.IntVar(&cpuFlag, "cpu", 0, "")
	fs.StringVar(&memS, "mem", "", "")
	fs.StringVar(&diskS, "disk", "", "")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if err := mgr.ValidateName(name); err != nil {
		return err
	}
	provided := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { provided[f.Name] = true })

	cpu, mem, disk := 1, 1024, 10
	setCpu, setMem, setDisk := provided["cpu"], provided["mem"], provided["disk"]
	var err error
	if setCpu {
		if err = validateCPU(strconv.Itoa(cpuFlag)); err != nil {
			return err
		}
		cpu = cpuFlag
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

	if inter.IsTTY() {
		if !setCpu {
			s, err := inter.Ask("CPU cores", "1", "", validateCPU)
			if err != nil {
				return err
			}
			cpu, _ = strconv.Atoi(s)
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
	res, err := m.Add(name, mgr.AddOptions{Password: passS, CPU: cpu, MemMB: mem, DiskGB: disk})
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
		return fmt.Errorf("usage: vpsmgr update <name> [--cpu 2] [--mem 2G] [--disk 20G]")
	}
	name := args[0]
	fs := flag.NewFlagSet("update", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	var cpuFlag int
	var memS, diskS string
	fs.IntVar(&cpuFlag, "cpu", 0, "")
	fs.StringVar(&memS, "mem", "", "")
	fs.StringVar(&diskS, "disk", "", "")
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
		if err = validateCPU(strconv.Itoa(cpuFlag)); err != nil {
			return err
		}
		cpu = cpuFlag
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

	if inter.IsTTY() {
		fmt.Printf("current quota: CPU %d / mem %d MiB / disk %d GiB\n", u.CPU, u.MemMB, u.DiskGB)
		if !setCpu {
			s, err := inter.Ask("new CPU cores", strconv.Itoa(cpu), "", validateCPU)
			if err != nil {
				return err
			}
			cpu, _ = strconv.Atoi(s)
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
	}

	if cpu == u.CPU && mem == u.MemMB && disk == u.DiskGB {
		if inter.IsTTY() {
			fmt.Println("no changes, exiting")
			return nil
		}
		return fmt.Errorf("nothing to update: pass at least one of --cpu/--mem/--disk")
	}
	if disk < u.DiskGB {
		return fmt.Errorf("disk can only grow: current %d GiB, cannot shrink to %d GiB", u.DiskGB, disk)
	}

	m := mgr.New(c, d)
	res, err := m.UpdateQuotas(name, cpu, mem, disk)
	if err != nil {
		return err
	}
	printResult(res)
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
		fmt.Printf("%-16s %-14s %-14s %-10s %-6d %-8d %-7d %-8s %-8s %-6s %-10s\n",
			r.User.Name, r.User.IP, fmt.Sprintf("%d-%d", r.User.PortBase, r.User.PortBase+r.PortsPerUser-1),
			r.State, r.User.CPU, r.User.MemMB, r.User.DiskGB, r.UpGB, r.DownGB, r.CPUUse, r.MemUse)
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
	fmt.Printf("ports:    %d-%d (ssh: %d)\n", u.PortBase, u.PortBase+r.PortsPerUser-1, u.PortBase)
	fmt.Printf("quotas:   %d cpu / %d MiB / %d GiB\n", u.CPU, u.MemMB, u.DiskGB)
	if r.IPv6 != "" {
		fmt.Printf("ipv6:     %s\n", r.IPv6)
	}
	if r.Password != "" {
		fmt.Printf("password: %s  (panel + root)\n", r.Password)
		fmt.Printf("ssh:      ssh -p %d root@%s\n", u.PortBase, r.PublicIP)
		c, _ := cfg.Load()
		fmt.Printf("panel:    https://%s:8443%s\n", r.PublicIP, panelPath(c))
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
	fmt.Printf("ports:    %d-%d (ssh: %d)\n", u.PortBase, u.PortBase+r.PortsPerUser-1, u.PortBase)
	fmt.Printf("quotas:   %d cpu / %d MiB / %d GiB\n", u.CPU, u.MemMB, u.DiskGB)
	fmt.Printf("cpu use:  %s\n", r.CPUUse)
	fmt.Printf("mem use:  %s\n", r.MemUse)
	fmt.Printf("traffic:  up %s GB / down %s GB (this month)\n", r.UpGB, r.DownGB)
	fmt.Printf("domains:  %s\n", strings.Join(r.Domains, ", "))
	if r.Password != "" {
		c, _ := cfg.Load()
		fmt.Printf("password: %s  (panel + root)\n", r.Password)
		fmt.Printf("ssh:      ssh -p %d root@%s\n", u.PortBase, r.PublicIP)
		fmt.Printf("panel:    https://%s:8443%s\n", r.PublicIP, panelPath(c))
	}
}

var reInt = regexp.MustCompile(`^\d+$`)

func validateCPU(s string) error {
	if !reInt.MatchString(s) {
		return fmt.Errorf("CPU cores must be an integer (no fractions)")
	}
	n, _ := strconv.Atoi(s)
	if n < 1 {
		return fmt.Errorf("CPU cores must be at least 1")
	}
	return nil
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
