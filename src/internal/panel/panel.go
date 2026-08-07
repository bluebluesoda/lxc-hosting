package panel

import (
	"embed"
	"html/template"
	"net/http"
	"strings"

	"vpsmgr/internal/cfg"
	"vpsmgr/internal/db"
	"vpsmgr/internal/mgr"
)

//go:embed templates/*.html
var tmplFS embed.FS

type Server struct {
	cfg *cfg.Config
	db  *db.DB
	mgr *mgr.Manager
}

func New(c *cfg.Config, d *db.DB, m *mgr.Manager) *Server {
	return &Server{cfg: c, db: d, mgr: m}
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
	t, err := s.templates()
	if err != nil {
		http.Error(w, "template error: "+err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
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
