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

	"vpsmgr/internal/cert"
	"vpsmgr/internal/cfg"
	"vpsmgr/internal/db"
	"vpsmgr/internal/fw"
	"vpsmgr/internal/inter"
	"vpsmgr/internal/mgr"
	"vpsmgr/internal/panel"
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
	case "user":
		err = cmdUser(os.Args[2:])
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
  vpsmgr install                  initialize panel (config/cert/db/systemd)
  vpsmgr serve                    run the web panel (systemd service)
  vpsmgr panel-url                print panel address
  vpsmgr user add <name> [--password X] [--cpu 1] [--mem 1G] [--disk 10G]
  vpsmgr user update <name> [--cpu 2] [--mem 2G] [--disk 20G]
  vpsmgr user reset-passwd <name>    # panel 密码重置为随机密码（显示一次）
  vpsmgr user start|stop|restart <name>
  vpsmgr user del <name>
  vpsmgr user list
  vpsmgr user show <name>
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
		c, err = cfg.Load()
		if err != nil {
			return err
		}
	} else {
		if err := c.FillAuto(); err != nil {
			return err
		}
		if err := cfg.Save(c); err != nil {
			return err
		}
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
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("daemon-reload: %s", strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "enable", "--now", "vpsmgr-nft.service").CombinedOutput(); err != nil {
		return fmt.Errorf("enable vpsmgr-nft: %s", strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "enable", "--now", "vpsmgr-panel.service").CombinedOutput(); err != nil {
		return fmt.Errorf("enable vpsmgr-panel: %s", strings.TrimSpace(string(out)))
	}
	fmt.Printf("panel initialized: https://%s:8443%s\n", c.Panel.PublicIP, panelPath(c))
	return nil
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
	d, err := db.Open(c.Panel.DB)
	if err != nil {
		return err
	}
	defer d.Close()
	m := mgr.New(c, d)
	srv := panel.New(c, d, m)
	tlsCfg := &tls.Config{MinVersion: tls.VersionTLS12}
	log.Printf("panel listening on %s (https, self-signed)", c.Panel.Listen)
	return startTLS(c, srv.Handler(), tlsCfg)
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
	fmt.Printf("https://%s:8443%s\n", c.Panel.PublicIP, panelPath(c))
	return nil
}

func cmdUser(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("user subcommand required: add|del|update|list|show")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "add":
		return userAdd(rest)
	case "del":
		if len(rest) != 1 {
			return fmt.Errorf("usage: vpsmgr user del <name>")
		}
		return userDel(rest[0])
	case "list":
		return userList()
	case "update":
		return userUpdate(rest)
	case "start", "stop", "restart":
		if len(rest) != 1 {
			return fmt.Errorf("usage: vpsmgr user %s <name>", sub)
		}
		return userPower(sub, rest[0])
	case "reset-passwd":
		if len(rest) != 1 {
			return fmt.Errorf("usage: vpsmgr user reset-passwd <name>")
		}
		return userResetPasswd(rest[0])
	case "show":
		if len(rest) != 1 {
			return fmt.Errorf("usage: vpsmgr user show <name>")
		}
		return userShow(rest[0])
	}
	return fmt.Errorf("unknown user subcommand: %s", sub)
}

func userAdd(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: vpsmgr user add <name> [--password X] [--cpu 1] [--mem 1G] [--disk 10G]")
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
			s, err := inter.Ask("CPU 核数", "1", "", validateCPU)
			if err != nil {
				return err
			}
			cpu, _ = strconv.Atoi(s)
		}
		if !setMem {
			s, err := inter.Ask("内存", "1024", " MiB (如 512 或 1G)", validateMem)
			if err != nil {
				return err
			}
			mem, _ = parseMemStrict(s)
		}
		if !setDisk {
			s, err := inter.Ask("磁盘", "10", " GiB", validateDisk)
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
	printResult(res)
	return nil
}

func userDel(name string) error {	c, err := cfg.Load()
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
		return fmt.Errorf("usage: vpsmgr user update <name> [--cpu 2] [--mem 2G] [--disk 20G]")
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
		fmt.Printf("当前配额: CPU %d 核 / 内存 %d MiB / 磁盘 %d GiB\n", u.CPU, u.MemMB, u.DiskGB)
		if !setCpu {
			s, err := inter.Ask("新 CPU 核数", strconv.Itoa(cpu), "", validateCPU)
			if err != nil {
				return err
			}
			cpu, _ = strconv.Atoi(s)
		}
		if !setMem {
			s, err := inter.Ask("新 内存", strconv.Itoa(mem), " MiB (如 512 或 1G)", validateMem)
			if err != nil {
				return err
			}
			mem, _ = parseMemStrict(s)
		}
		if !setDisk {
			s, err := inter.Ask("新 磁盘", strconv.Itoa(disk), " GiB (只允许扩容)", validateDisk)
			if err != nil {
				return err
			}
			disk, _ = parseDiskStrict(s)
		}
	}

	if cpu == u.CPU && mem == u.MemMB && disk == u.DiskGB {
		if inter.IsTTY() {
			fmt.Println("无变更，退出")
			return nil
		}
		return fmt.Errorf("nothing to update: pass at least one of --cpu/--mem/--disk")
	}
	if disk < u.DiskGB {
		return fmt.Errorf("磁盘只允许扩容：当前 %d GiB，不能缩到 %d GiB", u.DiskGB, disk)
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

func userList() error {	c, err := cfg.Load()
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
	fmt.Printf("%-16s %-14s %-14s %-10s %-6s %-8s %-7s %-6s %-10s\n", "NAME", "IP", "PORTS", "STATE", "CPU", "MEM", "DISK", "CPU%", "MEMUSE")
	for _, r := range results {
		fmt.Printf("%-16s %-14s %-14s %-10s %-6d %-8d %-7d %-6s %-10s\n",
			r.User.Name, r.User.IP, fmt.Sprintf("%d-%d", r.User.PortBase, r.User.PortBase+r.PortsPerUser-1),
			r.State, r.User.CPU, r.User.MemMB, r.User.DiskGB, r.CPUUse, r.MemUse)
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

func printResult(r *mgr.Result) {
	u := r.User
	fmt.Printf("name:     %s\n", u.Name)
	fmt.Printf("state:    %s\n", r.State)
	fmt.Printf("ip:       %s\n", u.IP)
	fmt.Printf("ports:    %d-%d (ssh: %d)\n", u.PortBase, u.PortBase+r.PortsPerUser-1, u.PortBase)
	fmt.Printf("quotas:   %d cpu / %d MiB / %d GiB\n", u.CPU, u.MemMB, u.DiskGB)
	fmt.Printf("cpu use:  %s\n", r.CPUUse)
	fmt.Printf("mem use:  %s\n", r.MemUse)
	fmt.Printf("domains:  %s\n", strings.Join(r.Domains, ", "))
	if r.Password != "" {
		c, _ := cfg.Load()
		fmt.Printf("password: %s  (panel login + container root)\n", r.Password)
		fmt.Printf("ssh:      ssh -p %d root@%s\n", u.PortBase, r.PublicIP)
		fmt.Printf("panel:    https://%s:8443%s\n", r.PublicIP, panelPath(c))
	}
}

var reInt = regexp.MustCompile(`^\d+$`)

func validateCPU(s string) error {
	if !reInt.MatchString(s) {
		return fmt.Errorf("CPU 核数必须是整数（不能是小数）")
	}
	n, _ := strconv.Atoi(s)
	if n < 1 {
		return fmt.Errorf("CPU 核数不能小于 1")
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
		return 0, fmt.Errorf("内存格式错误：应为整数 MiB（如 512）或整数后缀（如 1G）")
	}
	if !reInt.MatchString(s) {
		return 0, fmt.Errorf("内存必须是整数 MiB（如 512）或整数后缀（如 1G），不能是小数")
	}
	n, _ := strconv.Atoi(s)
	n *= mult
	if n < 64 {
		return 0, fmt.Errorf("内存不能小于 64 MiB")
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
		return 0, fmt.Errorf("磁盘必须是整数 GiB（如 10 或 10G），不能是小数")
	}
	n, _ := strconv.Atoi(s)
	if n < 1 {
		return 0, fmt.Errorf("磁盘不能小于 1 GiB")
	}
	return n, nil
}

func validateDisk(s string) error {
	_, err := parseDiskStrict(s)
	return err
}
