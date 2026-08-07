package panel

import (
	"embed"
	"html/template"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"vpsmgr/internal/cfg"
	"vpsmgr/internal/db"
	"vpsmgr/internal/mgr"
)

//go:embed templates/*.html
var tmplFS embed.FS

const (
	loginLimit    = 5
	loginWindow   = 60 * time.Second
	limiterMaxIPs = 10000
)

type loginRecord struct {
	start time.Time
	count int
}

type loginLimiter struct {
	mu    sync.Mutex
	byIP  map[string]*loginRecord
	limit int
	win   time.Duration
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{byIP: make(map[string]*loginRecord), limit: loginLimit, win: loginWindow}
}

// allowed increments the attempt counter for ip and reports whether the
// attempt may proceed (limit is attempts per window per IP).
func (l *loginLimiter) allowed(ip string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	w, ok := l.byIP[ip]
	if !ok || now.Sub(w.start) >= l.win {
		l.byIP[ip] = &loginRecord{start: now, count: 1}
		return true
	}
	w.count++
	if w.count > l.limit {
		return false
	}
	return true
}

// prune removes stale entries to bound memory.
func (l *loginLimiter) prune() {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.byIP) < limiterMaxIPs {
		return
	}
	for ip, w := range l.byIP {
		if now.Sub(w.start) >= l.win {
			delete(l.byIP, ip)
		}
	}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

type Server struct {
	cfg     *cfg.Config
	db      *db.DB
	mgr     *mgr.Manager
	limiter *loginLimiter
}

func New(c *cfg.Config, d *db.DB, m *mgr.Manager) *Server {
	return &Server{cfg: c, db: d, mgr: m, limiter: newLoginLimiter()}
}

func (s *Server) templates() (*template.Template, error) {
	return template.ParseFS(tmplFS, "templates/*.html")
}

type pageData struct {
	Title string
	User  *db.User
	State string
	IP    string
	PortBase int
	Ports string
	SSH   string
	Quota string
	Domains []string
	Msg   string
	Err   string
	PublicIP string
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/login", s.handleLogin)
	mux.HandleFunc("/logout", s.requireAuth(s.requirePost(s.handleLogout)))
	mux.HandleFunc("/", s.requireAuth(s.handleOverview))
	mux.HandleFunc("/power", s.requireAuth(s.requirePost(s.handlePower)))
	mux.HandleFunc("/reinstall", s.requireAuth(s.requirePost(s.handleReinstall)))
	mux.HandleFunc("/password", s.requireAuth(s.requirePost(s.handlePanelPassword)))
	mux.HandleFunc("/root-reset", s.requireAuth(s.requirePost(s.handleRootReset)))
	mux.HandleFunc("/domain-add", s.requireAuth(s.requirePost(s.handleDomainAdd)))
	mux.HandleFunc("/domain-del", s.requireAuth(s.requirePost(s.handleDomainDel)))
	return mux
}

func (s *Server) render(w http.ResponseWriter, name string, data pageData) {
	s.renderStatus(w, http.StatusOK, name, data)
}

func (s *Server) renderStatus(w http.ResponseWriter, status int, name string, data pageData) {
	t, err := s.templates()
	if err != nil {
		http.Error(w, "template error: "+err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	if err := t.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), 500)
	}
}

func (s *Server) redirect(w http.ResponseWriter, r *http.Request, path, msg string) {
	if msg != "" {
		path += "?msg=" + strings.ReplaceAll(msg, " ", "+")
	}
	http.Redirect(w, r, path, http.StatusFound)
}

func (s *Server) buildData(u *db.User, msg, errMsg string) pageData {
	d := pageData{
		Title:     "VPS Manager",
		User:      u,
		PublicIP:  s.cfg.Panel.PublicIP,
		PortBase:  u.PortBase,
		Ports:     portRange(u.PortBase, s.cfg.Net.PortsPerUser),
		SSH:       "ssh -p " + itoa(u.PortBase) + " root@" + s.cfg.Panel.PublicIP,
		Quota:     itoa(u.CPU) + " CPU / " + itoa(u.MemMB) + " MiB / " + itoa(u.DiskGB) + " GiB",
		Msg:       msg,
		Err:       errMsg,
	}
	st, err := s.mgr.State(u.Name)
	if err != nil {
		d.Err = err.Error()
	} else {
		d.State = st
		d.IP = u.IP
	}
	domains, _ := s.db.ListDomains(u.ID)
	for _, x := range domains {
		d.Domains = append(d.Domains, x.Domain)
	}
	return d
}

func portRange(base, n int) string {
	if n <= 1 {
		return itoa(base)
	}
	return itoa(base) + "-" + itoa(base+n-1)
}
