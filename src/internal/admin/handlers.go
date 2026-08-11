package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"vpsmgr/internal/cfg"
	"vpsmgr/internal/mgr"
	"vpsmgr/internal/pw"
)

type ctxKey int

const adminKey ctxKey = 0

// loginDummyHash is compared against when no admin password is set yet so the
// login takes the same time whether or not an admin exists.
var loginDummyHash = func() string { h, _ := pw.Hash("vpsmgr-admin-timing-pad"); return h }()

// pageData is the data handed to the admin templates.
type pageData struct {
	Title  string
	Prefix string
	Msg    string
	Err    string
	Host   hostView
	Reboot bool
	Users  []userView
	Lang   string
}

// hostView carries host memory/swap/pool/uptime numbers for the overview cards.
type hostView struct {
	MemTotal  string
	MemUsed   string
	MemPct    string
	SwapTotal string
	SwapUsed  string
	SwapPct   string
	PoolTotal string
	PoolUsed  string
	PoolAvail string
	PoolPct   string
	Uptime    string
}

// userView is one row of the admin user table.
type userView struct {
	Name     string
	State    string
	Ports    string
	SSHPort  string
	QuotaCPU string
	QuotaMem string
	QuotaDisk string
	CPUUse   string
	MemUse   string
	DiskUsed string
	UpGB     string
	DownGB   string
	IPv6     string
}

func (s *Server) buildPageData(msg, errMsg string) pageData {
	d := pageData{
		Title:  "VPS Manager Admin",
		Prefix: s.prefix(),
		Msg:    msg,
		Err:    errMsg,
	}
	hs := s.mgr.HostStats()
	d.Reboot = hs.RebootNeeded
	d.Host = hostView{
		MemTotal:  humanBytes(int64(hs.Mem.MemTotal)),
		MemUsed:   humanBytes(int64(hs.Mem.MemUsed)),
		SwapTotal: humanBytes(int64(hs.Mem.SwapTotal)),
		SwapUsed:  humanBytes(int64(hs.Mem.SwapUsed)),
		PoolTotal: humanBytes(hs.PoolTotal),
		PoolUsed:  humanBytes(hs.PoolUsed),
		PoolAvail: humanBytes(hs.PoolAvail),
		Uptime:    formatUptime(hs.Uptime),
	}
	if hs.Mem.MemTotal > 0 {
		d.Host.MemPct = strconv.Itoa(int(hs.Mem.MemUsed * 100 / hs.Mem.MemTotal)) + "%"
	}
	if hs.Mem.SwapTotal > 0 {
		d.Host.SwapPct = strconv.Itoa(int(hs.Mem.SwapUsed * 100 / hs.Mem.SwapTotal)) + "%"
	}
	if hs.PoolTotal > 0 {
		d.Host.PoolPct = strconv.Itoa(int(hs.PoolUsed * 100 / hs.PoolTotal)) + "%"
	}
	return d
}

func (s *Server) loadUsers(d *pageData) {
	statuses, err := s.mgr.BatchUsers()
	if err != nil {
		d.Err = d.Err + " " + err.Error()
		return
	}
	vs := make([]userView, 0, len(statuses))
	for _, st := range statuses {
		u := st.User
		vs = append(vs, userView{
			Name:      u.Name,
			State:     st.State,
			Ports:     mgr.ServicePorts(u.PortBase, s.cfg.Net.PortsPerUser),
			SSHPort:   strconv.Itoa(u.PortBase),
			QuotaCPU:  strconv.Itoa(u.CPU),
			QuotaMem:  strconv.Itoa(u.MemMB) + " MiB",
			QuotaDisk: strconv.Itoa(u.DiskGB) + " GiB",
			CPUUse:    st.CPUUse,
			MemUse:    st.MemUse,
			DiskUsed:  st.DiskUsed,
			UpGB:      st.UpGB,
			DownGB:    st.DownGB,
			IPv6:      st.IPv6,
		})
	}
	d.Users = vs
}

// formatUptime renders a duration as a static non-ticking string like
// "5d 3h 12m" so the admin panel shows the uptime captured at page load.
func formatUptime(d time.Duration) string {
	if d <= 0 {
		return "-"
	}
	d = d.Round(time.Minute)
	days := d / (24 * time.Hour)
	d -= days * 24 * time.Hour
	hours := d / time.Hour
	d -= hours * time.Hour
	mins := d / time.Minute
	if days > 0 {
		return strconv.FormatInt(int64(days), 10) + "d " + strconv.FormatInt(int64(hours), 10) + "h " + strconv.FormatInt(int64(mins), 10) + "m"
	}
	return strconv.FormatInt(int64(hours), 10) + "h " + strconv.FormatInt(int64(mins), 10) + "m"
}

// humanBytes renders a byte count as a short human string (e.g. "184 MiB").
func humanBytes(b int64) string {
	if b < 0 {
		b = 0
	}
	const unit = 1024
	if b < unit {
		return strconv.FormatInt(b, 10) + " B"
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return strconv.FormatFloat(float64(b)/float64(div), 'f', 1, 64) + " " + "KMGTPE"[exp:exp+1] + "iB"
}

// storeFlash persists a one-shot banner for the request's admin session.
func (s *Server) storeFlash(r *http.Request, msg, kind string) {
	if c, err := r.Cookie("vpsmgr_admin_session"); err == nil {
		if msg == "" {
			s.flash.Clear(c.Value)
			return
		}
		s.flash.Set(c.Value, msg, kind)
	}
}

// currentAdminHash reads the admin password hash fresh from the config file on
// every login. The CLI (`vpsmgr admin-passwd`) and the web UI both write the
// hash to the config, so this makes a CLI reset effective immediately without
// restarting the panel service. Login is low-frequency, so the extra read is
// negligible.
func (s *Server) currentAdminHash() string {
	if c, err := cfg.Load(); err == nil {
		return c.Panel.AdminPass
	}
	return s.cfg.Panel.AdminPass
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		ip := clientIP(r)
		s.limiter.prune()
		if !s.limiter.allowed(ip) {
			s.renderStatus(w, r, http.StatusTooManyRequests, "admin_login.html",
				pageData{Title: "Admin Login", Prefix: s.prefix(), Err: s.t(r, "err_too_many")})
			return
		}
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		pass := r.FormValue("password")
		// Password-only login: no username. Compare against the configured
		// bcrypt hash; when unset (fresh install before admin-passwd) burn the
		// same bcrypt time as a real compare.
		hash := s.currentAdminHash()
		if hash == "" {
			pw.Verify(loginDummyHash, pass)
			s.render(w, r, "admin_login.html", pageData{Title: "Admin Login", Prefix: s.prefix(), Err: s.t(r, "err_not_configured")})
			return
		}
		if !pw.Verify(hash, pass) {
			s.render(w, r, "admin_login.html", pageData{Title: "Admin Login", Prefix: s.prefix(), Err: s.t(r, "err_bad_login")})
			return
		}
		token, err := s.sessions.create(s.cfg.Panel.SessionDays)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		s.setSessionCookie(w, token)
		s.redirect(w, r, s.p(""), "")
		return
	}
	s.render(w, r, "admin_login.html", pageData{Title: "Admin Login", Prefix: s.prefix()})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("vpsmgr_admin_session"); err == nil {
		s.sessions.delete(c.Value)
	}
	s.clearSessionCookie(w)
	s.redirect(w, r, s.p("/login"), "")
}

// handleOverview renders the admin dashboard. It performs one full batch
// refresh (a handful of lxc calls) on every manual page load; there is no
// automatic polling.
func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	d := s.buildPageData("", "")
	s.loadUsers(&d)
	s.render(w, r, "admin_overview.html", d)
}

func (s *Server) handleFlash(w http.ResponseWriter, r *http.Request) {
	c, err := r.Cookie("vpsmgr_admin_session")
	if err != nil {
		http.Error(w, "", http.StatusUnauthorized)
		return
	}
	msg, kind, _ := s.flash.Pop(c.Value)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(struct {
		Msg  string `json:"msg"`
		Kind string `json:"kind"`
	}{msg, kind})
}

// handleUserAdd creates a user with the CLI's Add logic and shows the full
// login credentials (panel address, username, password) once in a modal.
func (s *Server) handleUserAdd(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	name := r.FormValue("name")
	cpu, err := strconv.Atoi(r.FormValue("cpu"))
	if err != nil {
		s.redirect(w, r, s.p(""), "error: "+s.t(r, "err_invalid_cpu"))
		return
	}
	memMB, err := parseMem(r.FormValue("mem"))
	if err != nil {
		s.redirect(w, r, s.p(""), "error: "+err.Error())
		return
	}
	diskGB, err := strconv.Atoi(r.FormValue("disk"))
	if err != nil {
		s.redirect(w, r, s.p(""), "error: "+s.t(r, "err_invalid_disk"))
		return
	}
	pass := r.FormValue("password")
	res, err := s.mgr.Add(name, mgr.AddOptions{Password: pass, CPU: cpu, MemMB: memMB, DiskGB: diskGB})
	if err != nil {
		s.redirect(w, r, s.p(""), "error: "+err.Error())
		return
	}
	cred := "user:      " + res.User.Name +
		"\npassword:  " + res.Password +
		"\npanel:     https://" + s.cfg.DisplayIP() + ":8443/" + s.cfg.Panel.URLPath
	s.redirectModal(w, r, s.p(""), cred)
}

func (s *Server) handleUserDel(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	name := r.FormValue("name")
	if r.FormValue("confirm") != "1" {
		s.redirect(w, r, s.p(""), "error: please confirm deletion")
		return
	}
	if err := s.mgr.Del(name); err != nil {
		s.redirect(w, r, s.p(""), "error: "+err.Error())
		return
	}
	s.redirect(w, r, s.p(""), s.t(r, "user_deleted", name))
}

func (s *Server) handleUserQuota(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	name := r.FormValue("name")
	cpu, err := strconv.Atoi(r.FormValue("cpu"))
	if err != nil {
		s.redirect(w, r, s.p(""), "error: "+s.t(r, "err_invalid_cpu"))
		return
	}
	memMB, err := parseMem(r.FormValue("mem"))
	if err != nil {
		s.redirect(w, r, s.p(""), "error: "+err.Error())
		return
	}
	diskGB, err := strconv.Atoi(r.FormValue("disk"))
	if err != nil {
		s.redirect(w, r, s.p(""), "error: "+s.t(r, "err_invalid_disk"))
		return
	}
	if _, err := s.mgr.UpdateQuotas(name, cpu, memMB, diskGB); err != nil {
		s.redirect(w, r, s.p(""), "error: "+err.Error())
		return
	}
	s.redirect(w, r, s.p(""), s.t(r, "quota_updated", name))
}

func (s *Server) handlePower(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	action := r.FormValue("action")
	name := r.FormValue("name")
	if err := s.mgr.Power(name, action); err != nil {
		s.redirect(w, r, s.p(""), "error: "+err.Error())
		return
	}
	s.redirect(w, r, s.p(""), s.t(r, "power_ok", name, action))
}

// handleResetPanelPass resets a user's panel login password and shows it once.
func (s *Server) handleResetPanelPass(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	name := r.FormValue("name")
	pass, err := s.mgr.ResetPanelPassword(name)
	if err != nil {
		s.redirect(w, r, s.p(""), "error: "+err.Error())
		return
	}
	panel := "https://" + s.cfg.DisplayIP() + ":8443/" + s.cfg.Panel.URLPath
	s.redirectModal(w, r, s.p(""), s.t(r, "new_panel_password", name, pass, panel))
}

// handleAdminPass changes the admin panel password (no username). The current
// session is preserved.
func (s *Server) handleAdminPass(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	pass := r.FormValue("new_password")
	confirm := r.FormValue("confirm_password")
	if pass != confirm {
		s.redirect(w, r, s.p(""), "error: "+s.t(r, "err_pass_mismatch"))
		return
	}
	if len(pass) < 14 {
		s.redirect(w, r, s.p(""), "error: "+s.t(r, "err_pass_short"))
		return
	}
	hash, err := pw.Hash(pass)
	if err != nil {
		s.redirect(w, r, s.p(""), "error: "+err.Error())
		return
	}
	s.cfg.Panel.AdminPass = hash
	if err := cfg.Save(s.cfg); err != nil {
		s.redirect(w, r, s.p(""), "error: "+err.Error())
		return
	}
	s.redirect(w, r, s.p(""), s.t(r, "admin_pass_changed"))
}

// parseMem parses a memory string ("512" or "1G") into MiB, mirroring the CLI.
func parseMem(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, strconv.ErrSyntax
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
		return 0, strconv.ErrSyntax
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, err
	}
	n *= mult
	if n < 64 {
		return 0, strconv.ErrSyntax
	}
	return n, nil
}
