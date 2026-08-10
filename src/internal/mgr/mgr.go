package mgr

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"vpsmgr/internal/cfg"
	"vpsmgr/internal/db"
	"vpsmgr/internal/fw"
	"vpsmgr/internal/lx"
	"vpsmgr/internal/pw"
	"vpsmgr/internal/tfx"
)

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

type Manager struct {
	cfg *cfg.Config
	db  *db.DB
	lx  *lx.Client
	fw  *fw.Firewall
	tfx *tfx.Traefik
}

func New(c *cfg.Config, d *db.DB) *Manager {
	return &Manager{cfg: c, db: d, lx: &lx.Client{}, fw: fw.New(c), tfx: tfx.New(c)}
}

func ValidateName(name string) error {
	if len(name) > 63 || !nameRe.MatchString(name) {
		return errors.New("invalid name: lowercase letters, digits and hyphens only (max 63)")
	}
	return nil
}

// PoolUsage returns the used ratio (0..1) of the storage pool as reported by
// LXD, or -1 if it cannot be determined.
func (m *Manager) PoolUsage() (float64, error) {
	out, err := exec.Command("lxc", "storage", "info", m.cfg.LXD.Pool).CombinedOutput()
	if err != nil {
		return -1, nil
	}
	used, ok1 := storageSpace(string(out), "space used:")
	total, ok2 := storageSpace(string(out), "total space:")
	if !ok1 || !ok2 || total <= 0 {
		return -1, nil
	}
	if used > total {
		used = total
	}
	return used / total, nil
}

func storageSpace(out, key string) (float64, bool) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, key) {
			return parseHumanBytes(strings.TrimSpace(strings.TrimPrefix(line, key)))
		}
	}
	return 0, false
}

var humanBytesRe = regexp.MustCompile(`^([0-9.]+)\s*([A-Za-z]?i?B|B)?$`)

func parseHumanBytes(s string) (float64, bool) {
	m := humanBytesRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return 0, false
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, false
	}
	units := map[string]float64{
		"B": 1, "KiB": 1 << 10, "MiB": 1 << 20, "GiB": 1 << 30, "TiB": 1 << 40,
		"KB": 1e3, "MB": 1e6, "GB": 1e9, "TB": 1e12,
	}
	mult, ok := units[m[2]]
	if !ok {
		mult = 1
	}
	return n * mult, true
}

// imageName returns the prebuilt image alias if it exists, else the fallback.
func (m *Manager) imageName() (string, error) {
	ok, _ := m.lx.ImageExists(m.cfg.LXD.Image)
	if ok {
		return m.cfg.LXD.Image, nil
	}
	return m.cfg.LXD.ImageFallback, nil
}

func rootPassScript(pass string) string {
	return fmt.Sprintf("printf 'root:%s\\n' | chpasswd\n", pass)
}

// Provision sets the root password and ensures sshd is running. If the image
// is the prebuilt sshd image only a light setup is done; otherwise sshd is
// installed on the fly.
func (m *Manager) Provision(name, image, pass string) error {
	if image == m.cfg.LXD.Image {
		script := rootPassScript(pass) + `
if command -v sshd >/dev/null 2>&1; then
  systemctl is-active ssh >/dev/null 2>&1 || systemctl start ssh
  systemctl enable ssh >/dev/null 2>&1 || true
fi`
		_, err := m.lx.ExecSH(name, script)
		return err
	}
	script := rootPassScript(pass) + `
export DEBIAN_FRONTEND=noninteractive
if ! command -v sshd >/dev/null 2>&1; then
  apt-get update -qq
  apt-get install -y -qq openssh-server
fi
mkdir -p /etc/ssh/sshd_config.d
printf 'PermitRootLogin yes\nPasswordAuthentication yes\n' > /etc/ssh/sshd_config.d/99-vpsmgr.conf
systemctl enable ssh >/dev/null 2>&1 || true
systemctl restart ssh`
	_, err := m.lx.ExecSH(name, script)
	return err
}

type AddOptions struct {
	Password string
	CPU      int
	MemMB    int
	DiskGB   int
}

func (m *Manager) Add(name string, opt AddOptions) (*Result, error) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	if opt.CPU < 1 {
		return nil, errors.New("cpu must be >= 1")
	}
	if opt.MemMB < 64 {
		return nil, errors.New("memory must be >= 64 MiB")
	}
	if opt.DiskGB < 1 {
		return nil, errors.New("disk must be >= 1 GiB")
	}
	if _, err := m.db.GetUserByName(name); err == nil {
		return nil, errors.New("user already exists: " + name)
	}
	if opt.Password == "" {
		opt.Password = pw.Generate(20)
	}
	hash, err := pw.Hash(opt.Password)
	if err != nil {
		return nil, err
	}
	idx, err := m.db.NextFreeIdx()
	if err != nil {
		return nil, err
	}
	usage, err := m.PoolUsage()
	if err != nil {
		return nil, err
	}
	if usage >= 0.9 {
		return nil, fmt.Errorf("storage pool %s is %.0f%% used (>= 90%%), refusing to create", m.cfg.LXD.Pool, usage*100)
	}
	image, err := m.imageName()
	if err != nil {
		return nil, err
	}
	ip := fmt.Sprintf("10.42.0.%d", idx+1)
	portBase := m.cfg.Net.PortBase + (idx-1)*m.cfg.Net.PortsPerUser
	ipv6, _ := m.IPv6Addr(name)
	if err := m.checkIPv6Collision(name, ipv6); err != nil {
		return nil, err
	}
	if err := m.lx.Launch(name, image, ip, ipv6, opt.CPU, opt.MemMB, opt.DiskGB); err != nil {
		return nil, fmt.Errorf("launch container: %w", err)
	}
	if err := m.Provision(name, image, opt.Password); err != nil {
		return nil, fmt.Errorf("provision container: %w", err)
	}
	// IPv6 pass-through: attach the /128 route + proxy_ndp entry for the
	// container's SLAAC global address (no-op when IPv6 is disabled).
	if err := m.WireIPv6(name); err != nil {
		return nil, fmt.Errorf("wire ipv6: %w", err)
	}
	u, err := m.db.CreateUser(name, hash, ip, idx, portBase, opt.CPU, opt.MemMB, opt.DiskGB)
	if err != nil {
		return nil, fmt.Errorf("db: %w", err)
	}
	if err := m.fw.WriteUser(name, u.IP, u.PortBase); err != nil {
		return nil, fmt.Errorf("write nft rules: %w", err)
	}
	if err := m.fw.Reload(); err != nil {
		return nil, err
	}
	return m.ResultFor(u, opt.Password), nil
}

// checkIPv6Collision refuses a new container if its deterministic IPv6 address
// already belongs to another user (32-bit hash space; ~1 in 135k for 253
// users, but a silent routing clash would be nasty to debug). No-op when IPv6
// is disabled.
func (m *Manager) checkIPv6Collision(name, addr string) error {
	if !m.cfg.IPv6Enabled() || addr == "" {
		return nil
	}
	users, err := m.db.ListUsers()
	if err != nil {
		return err
	}
	for _, u := range users {
		if u.Name == name {
			continue
		}
		v6, err := m.IPv6Addr(u.Name)
		if err != nil {
			return err
		}
		if v6 == addr {
			return fmt.Errorf("ipv6 address %s already assigned to user %q (hash collision); choose another name", addr, u.Name)
		}
	}
	return nil
}

type Result struct {
	User         *db.User
	Password     string
	PublicIP     string
	State        string
	Domains      []string
	PortsPerUser int
	CPUUse       string
	MemUse       string
	UpGB         string
	DownGB       string
	IPv6         string
}

// sampleUsage reads CPU/memory usage twice ~1s apart to derive CPU percentage.
func (m *Manager) sampleUsage() (map[string]lx.Usage, error) {
	u1, err := m.lx.UsageMap()
	if err != nil {
		return nil, err
	}
	time.Sleep(time.Second)
	u2, err := m.lx.UsageMap()
	if err != nil {
		return nil, err
	}
	out := make(map[string]lx.Usage)
	for name, v := range u2 {
		if prev, ok := u1[name]; ok {
			v.CPUUsage -= prev.CPUUsage
			out[name] = v
		}
	}
	return out, nil
}

func (m *Manager) decorateUsage(r *Result, use map[string]lx.Usage) {
	if u, ok := use[r.User.Name]; ok && r.User.CPU > 0 {
		pct := float64(u.CPUUsage) / 1e9 / float64(r.User.CPU) * 100
		if pct < 0 {
			pct = 0
		}
		r.CPUUse = fmt.Sprintf("%.0f%%", pct)
		r.MemUse = fmt.Sprintf("%d MiB", u.MemUsage/(1<<20))
		return
	}
	r.CPUUse = "-"
	r.MemUse = "-"
}

func (m *Manager) ResultFor(u *db.User, pass string) *Result {
	st, _ := m.lx.State(u.Name)
	domains, _ := m.db.ListDomains(u.ID)
	ds := make([]string, len(domains))
	for i, d := range domains {
		ds[i] = d.Domain
	}
	up, down := m.TrafficFor(u.ID)
	v6, _ := m.IPv6Addr(u.Name)
	return &Result{User: u, Password: pass, PublicIP: m.cfg.Panel.PublicIP,
		State: st, Domains: ds, PortsPerUser: m.cfg.Net.PortsPerUser,
		UpGB: FormatGB(up), DownGB: FormatGB(down), IPv6: v6}
}

func (m *Manager) Del(name string) error {
	if err := ValidateName(name); err != nil {
		return err
	}
	u, err := m.db.GetUserByName(name)
	if err != nil {
		return err
	}
	m.UnwireIPv6(name)
	if err := m.lx.Delete(name); err != nil {
		fmt.Printf("  ! warn: delete container: %v\n", err)
	}
	if err := m.fw.RemoveUser(name); err != nil {
		fmt.Printf("  ! warn: remove nft rules: %v\n", err)
	}
	if err := m.fw.Reload(); err != nil {
		fmt.Printf("  ! warn: reload nft: %v\n", err)
	}
	if err := m.tfx.RemoveUser(name); err != nil {
		fmt.Printf("  ! warn: remove traefik config: %v\n", err)
	}
	if err := m.db.DeleteUser(u.ID); err != nil {
		return err
	}
	return m.db.DeleteSessionsForUser(u.ID)
}

func (m *Manager) List() ([]*Result, error) {
	users, err := m.db.ListUsers()
	if err != nil {
		return nil, err
	}
	m.SampleTraffic() // best-effort freshness for the displayed totals
	use, _ := m.sampleUsage()
	out := make([]*Result, 0, len(users))
	for _, u := range users {
		r := m.ResultFor(u, "")
		m.decorateUsage(r, use)
		out = append(out, r)
	}
	return out, nil
}

func (m *Manager) Show(name string) (*Result, error) {
	u, err := m.db.GetUserByName(name)
	if err != nil {
		return nil, err
	}
	m.SampleTraffic() // best-effort freshness for the displayed totals
	r := m.ResultFor(u, "")
	use, _ := m.sampleUsage()
	m.decorateUsage(r, use)
	return r, nil
}

func (m *Manager) State(name string) (string, error) { return m.lx.State(name) }

// UpdateQuotas adjusts CPU/mem/disk of an existing user live (values <= 0 are
// left unchanged).
func (m *Manager) UpdateQuotas(name string, cpu, memMB, diskGB int) (*Result, error) {
	u, err := m.db.GetUserByName(name)
	if err != nil {
		return nil, err
	}
	if cpu > 0 {
		if err := m.lx.SetCPU(u.Name, cpu); err != nil {
			return nil, err
		}
		u.CPU = cpu
	}
	if memMB > 0 {
		if err := m.lx.SetMem(u.Name, memMB); err != nil {
			return nil, err
		}
		u.MemMB = memMB
	}
	if diskGB > 0 {
		if diskGB < u.DiskGB {
			return nil, fmt.Errorf("disk can only grow: current %d GiB, cannot shrink to %d GiB", u.DiskGB, diskGB)
		}
		if err := m.lx.SetDisk(u.Name, diskGB); err != nil {
			return nil, err
		}
		u.DiskGB = diskGB
	}
	if err := m.db.UpdateQuotas(u.ID, u.CPU, u.MemMB, u.DiskGB); err != nil {
		return nil, err
	}
	return m.ResultFor(u, ""), nil
}

func (m *Manager) Power(name, action string) error {	u, err := m.db.GetUserByName(name)
	if err != nil {
		return err
	}
	switch action {
	case "start":
		return m.lx.Start(u.Name)
	case "stop":
		return m.lx.Stop(u.Name)
	case "restart":
		return m.lx.Restart(u.Name)
	}
	return errors.New("unknown action")
}

// ChangePanelPassword updates only the panel login hash and invalidates all
// other sessions of the user. keepToken is the current session token to
// preserve (empty means none, so every session is dropped). Container root
// password is managed separately via ResetRootPassword.
func (m *Manager) ChangePanelPassword(name, pass, keepToken string) error {
	u, err := m.db.GetUserByName(name)
	if err != nil {
		return err
	}
	hash, err := pw.Hash(pass)
	if err != nil {
		return err
	}
	if err := m.db.UpdatePassword(u.ID, hash); err != nil {
		return err
	}
	return m.db.DeleteSessionsForUserExcept(u.ID, keepToken)
}

// ResetPanelPassword sets the panel login password to a new random 20-char
// value, drops all existing sessions of the user and returns the password for
// one-time display. Container root password is untouched.
func (m *Manager) ResetPanelPassword(name string) (string, error) {
	u, err := m.db.GetUserByName(name)
	if err != nil {
		return "", err
	}
	pass := pw.Generate(20)
	if err := m.ChangePanelPassword(u.Name, pass, ""); err != nil {
		return "", err
	}
	return pass, nil
}

// ResetRootPassword sets the container root password to a new random 20-char
// value and returns it for one-time display. The panel hash is not touched.
func (m *Manager) ResetRootPassword(name string) (string, error) {
	u, err := m.db.GetUserByName(name)
	if err != nil {
		return "", err
	}
	image, err := m.imageName()
	if err != nil {
		return "", err
	}
	pass := pw.Generate(20)
	if err := m.Provision(u.Name, image, pass); err != nil {
		return "", err
	}
	return pass, nil
}

// Reinstall destroys and recreates the container keeping IP/ports/domains.
// A new random root password is generated (returned for one-time display);
// the panel login password is unchanged.
func (m *Manager) Reinstall(name string) (string, error) {
	u, err := m.db.GetUserByName(name)
	if err != nil {
		return "", err
	}
	if err := m.lx.Delete(u.Name); err != nil {
		return "", fmt.Errorf("delete container: %w", err)
	}
	image, err := m.imageName()
	if err != nil {
		return "", err
	}
	ipv6, _ := m.IPv6Addr(u.Name)
	if err := m.lx.Launch(u.Name, image, u.IP, ipv6, u.CPU, u.MemMB, u.DiskGB); err != nil {
		return "", fmt.Errorf("recreate container: %w", err)
	}
	pass := pw.Generate(20)
	if err := m.Provision(u.Name, image, pass); err != nil {
		return "", fmt.Errorf("provision container: %w", err)
	}
	if err := m.WireIPv6(u.Name); err != nil {
		return "", fmt.Errorf("wire ipv6: %w", err)
	}
	return pass, nil
}

func (m *Manager) AddDomain(name, domain string) error {
	u, err := m.db.GetUserByName(name)
	if err != nil {
		return err
	}
	domain, err = normalizeDomain(domain)
	if err != nil {
		return err
	}
	if exists, err := m.db.DomainExists(domain); err != nil {
		return err
	} else if exists {
		return errors.New("domain already bound")
	}
	if _, err := m.db.AddDomain(u.ID, domain); err != nil {
		return err
	}
	return m.syncTraefik(u)
}

func (m *Manager) DelDomain(name, domain string) error {
	u, err := m.db.GetUserByName(name)
	if err != nil {
		return err
	}
	domain, err = normalizeDomain(domain)
	if err != nil {
		return err
	}
	if err := m.db.DeleteDomain(u.ID, domain); err != nil {
		return err
	}
	return m.syncTraefik(u)
}

func (m *Manager) syncTraefik(u *db.User) error {
	domains, err := m.db.ListDomains(u.ID)
	if err != nil {
		return err
	}
	if len(domains) == 0 {
		return m.tfx.RemoveUser(u.Name)
	}
	ds := make([]string, len(domains))
	for i, d := range domains {
		ds[i] = d.Domain
	}
	return m.tfx.WriteUser(u.Name, u.IP, ds)
}

// domainRe allows only lowercase letters, digits, dots and hyphens, starting
// with an alphanumeric. The trailing letter check is applied separately.
var domainRe = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]*$`)

// normalizeDomain validates and normalizes a domain for use in Traefik
// dynamic YAML. Strictly limited to [a-z0-9.-] and must end with a letter so
// it cannot break YAML syntax or inject extra Traefik config.
func normalizeDomain(d string) (string, error) {
	d = strings.TrimSpace(strings.ToLower(d))
	d = strings.TrimSuffix(d, ".")
	if d == "" {
		return "", errors.New("domain empty")
	}
	if len(d) > 253 {
		return "", errors.New("domain too long")
	}
	if !domainRe.MatchString(d) {
		return "", errors.New("invalid domain: only letters, digits, dots and hyphens allowed")
	}
	if last := d[len(d)-1]; last < 'a' || last > 'z' {
		return "", errors.New("invalid domain: must end with a letter")
	}
	return d, nil
}
