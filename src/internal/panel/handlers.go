package panel

import (
	"context"
	"net/http"
	"strconv"

	"vpsmgr/internal/db"
	"vpsmgr/internal/pw"
)

func itoa(n int) string { return strconv.Itoa(n) }

type ctxKey int

const userKey ctxKey = 0

func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("vpsmgr_session")
		if err != nil {
			s.redirect(w, r, "/login", "")
			return
		}
		u, err := s.db.SessionUser(c.Value)
		if err != nil {
			s.clearSessionCookie(w)
			s.redirect(w, r, "/login", "")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), userKey, u)))
	}
}

// requirePost rejects everything but POST so no state-changing action can be
// triggered by a top-level GET navigation (CSRF via SameSite=Lax).
func (s *Server) requirePost(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		next(w, r)
	}
}

func (s *Server) currentUser(r *http.Request) *db.User {
	u, _ := r.Context().Value(userKey).(*db.User)
	return u
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     "vpsmgr_session",
		Value:    token,
		Path:     "/",
		MaxAge:   s.cfg.Panel.SessionDays * 86400,
		HttpOnly: true,
		Secure:   true,
		SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "vpsmgr_session",
		Value:    "",
		MaxAge:   -1,
		Path:     "/",
		HttpOnly: true,
		Secure:   true,
	})
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		name := r.FormValue("username")
		pass := r.FormValue("password")
		u, err := s.db.GetUserByName(name)
		if err == nil && pw.Verify(u.PassHash, pass) {
			sess, err := s.db.CreateSession(u.ID, s.cfg.Panel.SessionDays)
			if err == nil {
				s.setSessionCookie(w, sess.Token)
				s.redirect(w, r, "/", "")
				return
			}
		}
		s.render(w, "login.html", pageData{Title: "Login", Err: "invalid credentials"})
		return
	}
	s.render(w, "login.html", pageData{Title: "Login"})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie("vpsmgr_session"); err == nil {
		s.db.DeleteSession(c.Value)
	}
	s.clearSessionCookie(w)
	s.redirect(w, r, "/login", "")
}

func (s *Server) handleOverview(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	msg := r.URL.Query().Get("msg")
	s.render(w, "overview.html", s.buildData(u, msg, ""))
}

func (s *Server) handlePower(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	action := r.FormValue("action")
	var msg string
	if err := s.mgr.Power(u.Name, action); err != nil {
		msg = "error: " + err.Error()
	} else {
		msg = "ok: " + action
	}
	s.redirect(w, r, "/", msg)
}

func (s *Server) handleReinstall(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if r.FormValue("confirm") != "1" {
		s.redirect(w, r, "/", "error: please confirm reinstall")
		return
	}
	pass, err := s.mgr.Reinstall(u.Name)
	if err != nil {
		s.redirect(w, r, "/", "error: "+err.Error())
		return
	}
	msg := "reinstalled. new root password: " + pass + " (shown once)"
	s.redirect(w, r, "/", msg)
}

// handlePanelPassword changes only the panel login password (must be > 14
// chars) and kicks all other sessions. Container root password is untouched.
func (s *Server) handlePanelPassword(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	pass := r.FormValue("new_password")
	if len(pass) <= 14 {
		s.redirect(w, r, "/", "error: panel password must be longer than 14 characters")
		return
	}
	token := ""
	if c, err := r.Cookie("vpsmgr_session"); err == nil {
		token = c.Value
	}
	if err := s.mgr.ChangePanelPassword(u.Name, pass, token); err != nil {
		s.redirect(w, r, "/", "error: "+err.Error())
		return
	}
	s.redirect(w, r, "/", "ok: panel password changed")
}

// handleRootReset regenerates the container root password and shows it once.
func (s *Server) handleRootReset(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	pass, err := s.mgr.ResetRootPassword(u.Name)
	if err != nil {
		s.redirect(w, r, "/", "error: "+err.Error())
		return
	}
	msg := "root password reset: " + pass + " (shown once)"
	s.redirect(w, r, "/", msg)
}

func (s *Server) handleDomainAdd(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.mgr.AddDomain(u.Name, r.FormValue("domain")); err != nil {
		s.redirect(w, r, "/", "error: "+err.Error())
		return
	}
	s.redirect(w, r, "/", "ok: domain added")
}

func (s *Server) handleDomainDel(w http.ResponseWriter, r *http.Request) {
	u := s.currentUser(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if err := s.mgr.DelDomain(u.Name, r.FormValue("domain")); err != nil {
		s.redirect(w, r, "/", "error: "+err.Error())
		return
	}
	s.redirect(w, r, "/", "ok: domain removed")
}
