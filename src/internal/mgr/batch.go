package mgr

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"vpsmgr/internal/db"
)

// HostMem describes the host's physical memory and swap usage.
type HostMem struct {
	MemTotal uint64 // bytes
	MemUsed  uint64 // bytes
	SwapTotal uint64 // bytes
	SwapUsed uint64 // bytes
}

// HostStats is the admin panel's host overview: memory, swap, pool space and
// whether a reboot is pending (e.g. Ubuntu unattended-upgrades or livepatch
// staged a kernel update).
type HostStats struct {
	Mem          HostMem
	PoolTotal    int64  // bytes
	PoolUsed     int64  // bytes
	PoolAvail    int64  // bytes
	RebootNeeded bool
}

// PoolRemainingBytes returns the pool's total/used/available bytes via
// `lxc storage info <pool>`. The human format (e.g. "182.40MiB") is parsed by
// the same helper PoolUsage uses — NOT the --bytes variant, whose values are
// quoted and unit-less and would break parseHumanBytes.
func (m *Manager) PoolRemainingBytes() (total, used, avail int64, err error) {
	out, err := exec.Command("lxc", "storage", "info", m.cfg.LXD.Pool).CombinedOutput()
	if err != nil {
		return 0, 0, 0, fmt.Errorf("lxc storage info %s: %s", m.cfg.LXD.Pool, strings.TrimSpace(string(out)))
	}
	totalF, ok1 := storageSpace(string(out), "total space:")
	usedF, ok2 := storageSpace(string(out), "space used:")
	if !ok1 || !ok2 || totalF <= 0 {
		return 0, 0, 0, fmt.Errorf("could not parse storage info for pool %s", m.cfg.LXD.Pool)
	}
	total, used = int64(totalF), int64(usedF)
	avail = total - used
	if avail < 0 {
		avail = 0
	}
	return total, used, avail, nil
}

// HostStats gathers host memory/swap from /proc/meminfo and pool space from
// LXD. Pool failures are non-fatal (zeroed) so the panel still renders the
// memory + reboot sections.
func (m *Manager) HostStats() HostStats {
	hs := HostStats{}
	hs.Mem = readMemInfo()
	total, used, avail, err := m.PoolRemainingBytes()
	if err == nil {
		hs.PoolTotal, hs.PoolUsed, hs.PoolAvail = total, used, avail
	}
	hs.RebootNeeded = rebootRequired()
	return hs
}

// readMemInfo parses /proc/meminfo into usable/available memory and swap.
func readMemInfo() HostMem {
	var m HostMem
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return m
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	kb := func(k string) uint64 {
		var v uint64
		fmt.Sscanf(k, "%d", &v)
		return v * 1024
	}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "MemTotal:"):
			m.MemTotal = kb(strings.TrimSpace(strings.TrimPrefix(line, "MemTotal:")))
		case strings.HasPrefix(line, "MemAvailable:"):
			m.MemUsed = m.MemTotal - kb(strings.TrimSpace(strings.TrimPrefix(line, "MemAvailable:")))
		case strings.HasPrefix(line, "SwapTotal:"):
			m.SwapTotal = kb(strings.TrimSpace(strings.TrimPrefix(line, "SwapTotal:")))
		case strings.HasPrefix(line, "SwapFree:"):
			m.SwapUsed = m.SwapTotal - kb(strings.TrimSpace(strings.TrimPrefix(line, "SwapFree:")))
		}
	}
	return m
}

// rebootRequired reports whether /var/run/reboot-required exists (set by
// Ubuntu unattended-upgrades / livepatch when a kernel update needs a reboot).
func rebootRequired() bool {
	_, err := os.Stat("/var/run/reboot-required")
	return err == nil
}

// UserStatus is one row of the admin user table: the DB user record plus live
// stats (state, CPU%, memory usage, actual disk usage, monthly traffic).
type UserStatus struct {
	User     *db.User
	State    string
	CPUUse   string // e.g. "12%" or "-"
	MemUse   string // e.g. "345 MiB" or "-"
	DiskUsed string // actual disk usage, e.g. "184 MiB" or "-"
	UpGB     string
	DownGB   string
	IPv6     string
}

// BatchUsers returns live status for every user with a MINIMAL number of LXD
// calls regardless of user count:
//   - 1 x `lxc list --format=json` (SampleTraffic, also freshens monthly totals)
//   - 2 x `lxc list --format=json` ~1s apart (CPU% delta, same algorithm as List)
//   - 1 x `lxc exec <name> -- df -k /` per RUNNING container for real disk usage
//
// The per-container disk probe runs concurrently so the total time is bounded
// by the slowest container, not the count.
func (m *Manager) BatchUsers() ([]*UserStatus, error) {
	users, err := m.db.ListUsers()
	if err != nil {
		return nil, err
	}
	m.SampleTraffic() // best-effort freshness for the displayed totals

	s1, err := m.lx.Containers()
	if err != nil {
		return nil, err
	}
	time.Sleep(time.Second)
	s2, err := m.lx.Containers()
	if err != nil {
		return nil, err
	}

	// Concurrent real disk usage per running container.
	disk := make(map[string]int64)
	var diskMu sync.Mutex
	var wg sync.WaitGroup
	for _, u := range users {
		if s1[u.Name].Status != "Running" {
			continue
		}
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			if kb, err := containerDiskKB(name); err == nil && kb >= 0 {
				diskMu.Lock()
				disk[name] = kb * 1024
				diskMu.Unlock()
			}
		}(u.Name)
	}
	wg.Wait()

	out := make([]*UserStatus, 0, len(users))
	for _, u := range users {
		cur := s2[u.Name]
		prev, ok := s1[u.Name]
		up, down := m.TrafficFor(u.ID)
		rs := &UserStatus{User: u, State: cur.Status, UpGB: FormatGB(up), DownGB: FormatGB(down)}
		if ok && cur.Status == "Running" && prev.Status == "Running" && u.CPU > 0 {
			delta := cur.CPUUsage - prev.CPUUsage
			if delta < 0 {
				delta = 0
			}
			pct := float64(delta) / 1e9 / float64(u.CPU) * 100
			rs.CPUUse = fmt.Sprintf("%.0f%%", pct)
			rs.MemUse = humanBytes(cur.MemUsage)
		} else {
			rs.CPUUse = "-"
			rs.MemUse = "-"
		}
		if kb, ok := disk[u.Name]; ok && kb > 0 {
			rs.DiskUsed = humanBytes(kb)
		} else {
			rs.DiskUsed = "-"
		}
		if v6, err := m.IPv6Addr(u.Name); err == nil {
			rs.IPv6 = v6
		}
		out = append(out, rs)
	}
	return out, nil
}

// containerDiskKB returns the used kilobytes of the container's root
// filesystem as reported by `df -k /` inside the container. The first data row
// of df carries the mount point (/) in its LAST column, the total 1K-blocks in
// the second and the Used KB in the THIRD; the filesystem name (e.g.
// vpsmgr/containers/test) is not a reliable marker across storage drivers.
func containerDiskKB(name string) (int64, error) {
	out, err := exec.Command("lxc", "exec", name, "--", "df", "-k", "/").CombinedOutput()
	if err != nil {
		return -1, err
	}
	sc := bufio.NewScanner(strings.NewReader(string(out)))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) >= 6 && f[0] != "Filesystem" && f[len(f)-1] == "/" {
			n, err := strconv.ParseInt(f[2], 10, 64)
			if err != nil {
				return -1, err
			}
			return n, nil
		}
	}
	return -1, fmt.Errorf("no df output for %s", name)
}

// humanBytes renders a byte count as a short human string (e.g. "184 MiB").
func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
