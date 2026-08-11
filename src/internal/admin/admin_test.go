package admin

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"vpsmgr/internal/cfg"
	"vpsmgr/internal/db"
	"vpsmgr/internal/mgr"
	"vpsmgr/internal/pw"
)

const testAdminSecret = "Adm1n-SecretX"

// newTestServer builds an admin Server against a temp DB and points
// VPSMGR_CONFIG at a temp config file so the per-login disk read in
// currentAdminHash() stays isolated from the host's real config.
func newTestServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	cfgPath := t.TempDir() + "/config.yaml"
	t.Setenv("VPSMGR_CONFIG", cfgPath)
	c := cfg.Default()
	c.Panel.URLPath = "UserSecRet99"
	c.Panel.AdminPath = testAdminSecret
	c.Panel.PublicIP = "127.0.0.1"
	c.Panel.SessionDays = 3
	if err := cfg.Save(c); err != nil {
		t.Fatal(err)
	}
	d, err := db.Open(t.TempDir() + "/test.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	return New(c, d, mgr.New(c, d)), d
}

// setAdminPass stores the admin password hash both in the in-memory config and
// in the temp config file, mirroring what `vpsmgr admin-passwd` writes.
func setAdminPass(t *testing.T, srv *Server, pass string) {
	t.Helper()
	srv.cfg.Panel.AdminPass = mustHash(t, pass)
	if err := cfg.Save(srv.cfg); err != nil {
		t.Fatal(err)
	}
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
	setAdminPass(t, srv, "correct-horse-battery")
	h := srv.Handler()
	prefix := "/" + testAdminSecret

	// Login page renders.
	rr := doReq(t, h, http.MethodGet, prefix+"/login", nil, nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "Admin") {
		t.Fatalf("GET %s/login = %d, want login page", prefix, rr.Code)
	}
	// Wrong password: no redirect, error shown (English by default).
	rr = doReq(t, h, http.MethodPost, prefix+"/login", url.Values{"password": {"nope"}}, nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "invalid admin password") {
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
	setAdminPass(t, srv, "old-pass-12345678")
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
	setAdminPass(t, srv, "logout-pass-12345")
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

// TestLanguageSwitch verifies the admin language is resolved from ?lang=, the
// cookie and the browser header (mirroring the user panel), and that an
// explicit ?lang= choice persists in a scoped cookie.
func TestFormatUptime(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{0, "-"},
		{-1 * time.Second, "-"},
		{5 * time.Minute, "0h 5m"},
		{90 * time.Minute, "1h 30m"},
		{48*time.Hour + 73*time.Minute, "2d 1h 13m"},
		{10*24*time.Hour + 3*time.Hour + 29*time.Minute, "10d 3h 29m"},
	}
	for _, c := range cases {
		if got := formatUptime(c.d); got != c.want {
			t.Errorf("formatUptime(%v) = %q, want %q", c.d, got, c.want)
		}
	}
}

func TestLanguageSwitch(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	prefix := "/" + testAdminSecret

	// zh browser -> zh login page.
	req := httptest.NewRequest(http.MethodGet, prefix+"/login", nil)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if !strings.Contains(rr.Body.String(), "管理员登录") {
		t.Fatalf("zh login page missing Chinese title")
	}

	// English browser (or no header) -> en page.
	rr = doReq(t, h, http.MethodGet, prefix+"/login", nil, nil)
	if !strings.Contains(rr.Body.String(), "Admin Login") {
		t.Fatalf("default login page missing English title")
	}

	// Explicit ?lang=en on a zh browser wins and sets the cookie.
	req = httptest.NewRequest(http.MethodGet, prefix+"/login?lang=en", nil)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if !strings.Contains(rr.Body.String(), "Admin Login") {
		t.Fatalf("?lang=en did not switch the page to English")
	}
	var langCookieFound *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == langCookie {
			v := *c
			langCookieFound = &v
		}
	}
	if langCookieFound == nil || langCookieFound.Value != langEn {
		t.Fatalf("?lang=en did not persist the %s cookie", langCookie)
	}

	// The cookie overrides the zh browser header.
	req = httptest.NewRequest(http.MethodGet, prefix+"/login", nil)
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.AddCookie(langCookieFound)
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if !strings.Contains(rr.Body.String(), "Admin Login") {
		t.Fatalf("cookie did not override browser language")
	}
}
