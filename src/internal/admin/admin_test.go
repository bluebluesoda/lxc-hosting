package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"vpsmgr/internal/cfg"
	"vpsmgr/internal/db"
	"vpsmgr/internal/mgr"
	"vpsmgr/internal/pw"
)

const testAdminSecret = "Adm1n-SecretX"

func newTestServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	c := cfg.Default()
	c.Panel.URLPath = "UserSecRet99"
	c.Panel.AdminPath = testAdminSecret
	c.Panel.PublicIP = "127.0.0.1"
	c.Panel.SessionDays = 3
	d, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return New(c, d, mgr.New(c, d)), d
}

func doReq(t *testing.T, h http.Handler, method, target string, form url.Values, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var body io.Reader
	if form != nil {
		body = strings.NewReader(form.Encode())
	}
	req := httptest.NewRequest(method, target, body)
	if form != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

func adminLogin(t *testing.T, h http.Handler, prefix, pass string) *http.Cookie {
	t.Helper()
	rr := doReq(t, h, http.MethodPost, prefix+"/login", url.Values{"password": {pass}}, nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("admin login = %d, want 302 (body %s)", rr.Code, rr.Body.String())
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == "vpsmgr_admin_session" {
			return c
		}
	}
	t.Fatal("no admin session cookie")
	return nil
}

func TestAdminLoginAndSession(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.cfg.Panel.AdminPass = mustHash(t, "correct-horse-battery")
	h := srv.Handler()
	prefix := "/" + testAdminSecret

	// Login page renders.
	rr := doReq(t, h, http.MethodGet, prefix+"/login", nil, nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Admin") {
		t.Fatalf("GET %s/login = %d, want login page", prefix, rr.Code)
	}
	// Wrong password: no redirect, error shown.
	rr = doReq(t, h, http.MethodPost, prefix+"/login", url.Values{"password": {"nope"}}, nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "invalid password") {
		t.Fatalf("bad admin login: code=%d body=%s", rr.Code, rr.Body.String())
	}
	// Correct password: 302 + session cookie scoped to admin prefix.
	rr = doReq(t, h, http.MethodPost, prefix+"/login", url.Values{"password": {"correct-horse-battery"}}, nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("admin login = %d, want 302", rr.Code)
	}
	var cookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == "vpsmgr_admin_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no admin session cookie")
	}
	if cookie.Path != prefix {
		t.Fatalf("cookie Path = %q, want %q", cookie.Path, prefix)
	}
	// Overview with the cookie renders the admin dashboard (no users yet).
	rr = doReq(t, h, http.MethodGet, prefix, nil, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Admin") {
		t.Fatalf("GET %s with session = %d, want overview", prefix, rr.Code)
	}
}

func TestAdminRequiresSession(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	prefix := "/" + testAdminSecret
	// Without a session the admin root redirects to the admin login.
	rr := doReq(t, h, http.MethodGet, prefix, nil, nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("GET %s = %d, want redirect", prefix, rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != prefix+"/login" {
		t.Fatalf("Location = %q, want %q", loc, prefix+"/login")
	}
}

func TestAdminPasswordChange(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.cfg.Panel.AdminPass = mustHash(t, "old-pass-12345678")
	h := srv.Handler()
	prefix := "/" + testAdminSecret
	sess := adminLogin(t, h, prefix, "old-pass-12345678")

	// Mismatched confirmation -> redirect with flash, no change.
	rr := doReq(t, h, http.MethodPost, prefix+"/admin-pass",
		url.Values{"new_password": {"new-pass-123456789"}, "confirm_password": {"different-12345"}}, sess)
	if rr.Code != http.StatusFound {
		t.Fatalf("admin-pass mismatch = %d, want 302", rr.Code)
	}
	if !pw.Verify(srv.cfg.Panel.AdminPass, "old-pass-12345678") {
		t.Fatal("password changed despite mismatch")
	}
	// Successful change persists the new hash in the config object.
	rr = doReq(t, h, http.MethodPost, prefix+"/admin-pass",
		url.Values{"new_password": {"new-pass-123456789"}, "confirm_password": {"new-pass-123456789"}}, sess)
	if rr.Code != http.StatusFound {
		t.Fatalf("admin-pass = %d, want 302", rr.Code)
	}
	if !pw.Verify(srv.cfg.Panel.AdminPass, "new-pass-123456789") {
		t.Fatal("new password hash not stored")
	}
}

func TestAdminLogout(t *testing.T) {
	srv, _ := newTestServer(t)
	srv.cfg.Panel.AdminPass = mustHash(t, "logout-pass-12345")
	h := srv.Handler()
	prefix := "/" + testAdminSecret
	sess := adminLogin(t, h, prefix, "logout-pass-12345")

	rr := doReq(t, h, http.MethodPost, prefix+"/logout", nil, sess)
	if rr.Code != http.StatusFound {
		t.Fatalf("logout = %d, want 302", rr.Code)
	}
	// Session is gone: root now redirects to login again.
	rr = doReq(t, h, http.MethodGet, prefix, nil, sess)
	if rr.Code != http.StatusFound || rr.Header().Get("Location") != prefix+"/login" {
		t.Fatalf("after logout, GET %s = %d (loc %q), want redirect to login", prefix, rr.Code, rr.Header().Get("Location"))
	}
}

func TestSessionStoreExpiry(t *testing.T) {
	s := newSessionStore(10)
	tok, err := s.create(0) // 0 days -> expires immediately
	if err != nil {
		t.Fatal(err)
	}
	if s.valid(tok) {
		t.Fatal("zero-length session should be expired")
	}
	tok2, err := s.create(3)
	if err != nil {
		t.Fatal(err)
	}
	if !s.valid(tok2) {
		t.Fatal("3-day session should be valid")
	}
	s.delete(tok2)
	if s.valid(tok2) {
		t.Fatal("deleted session should be invalid")
	}
}

func TestAdminFeatureless404OutsidePrefix(t *testing.T) {
	// The admin handler itself must never serve content off its prefix. The
	// full dispatcher (user + admin + 404) lives in main; here we assert the
	// admin handler returns a bare 404 for non-prefixed paths.
	srv, _ := newTestServer(t)
	h := srv.Handler()
	for _, p := range []string{"/", "/login", "/admin", "/" + testAdminSecret + "x"} {
		rr := doReq(t, h, http.MethodGet, p, nil, nil)
		if rr.Code != http.StatusNotFound {
			t.Fatalf("GET %s = %d, want bare 404", p, rr.Code)
		}
		if body := rr.Body.String(); body != "" {
			t.Fatalf("GET %s body = %q, want empty", p, body)
		}
	}
}

func mustHash(t *testing.T, pass string) string {
	t.Helper()
	h, err := pw.Hash(pass)
	if err != nil {
		t.Fatal(err)
	}
	return h
}
