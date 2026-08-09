package panel

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

const testSecret = "Ab1_cdE-9x"

func newTestServer(t *testing.T) (*Server, *db.DB) {
	t.Helper()
	c := cfg.Default()
	c.Panel.URLPath = testSecret
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

// featureless asserts a bare 404: empty body, no Content-Type and no headers
// at all, identical for every wrong path so scanners learn nothing. Any header
// (including security headers) would fingerprint the service.
func featureless(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}
	if body := rr.Body.String(); body != "" {
		t.Fatalf("body = %q, want empty", body)
	}
	if n := len(rr.Header()); n != 0 {
		t.Fatalf("headers = %v, want none (headers fingerprint the service)", rr.Header())
	}
}

func TestFeatureless404ForWrongPaths(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	paths := []string{
		"/",
		"/login",
		"/admin",
		"/robots.txt",
		"/favicon.ico",
		"/" + testSecret + "x", // near-miss prefix
		"/" + strings.ToUpper(testSecret),
		"//" + testSecret,
		"/secret/../" + testSecret,
		"/?q=1",
		"/anything/else",
	}
	// Every real route must be unreachable without the secret prefix.
	for _, route := range []string{"/login", "/logout", "/power", "/reinstall", "/password", "/root-reset", "/domain-add", "/domain-del", "/flash"} {
		paths = append(paths, route)
	}
	for _, p := range paths {
		rr := doReq(t, h, http.MethodGet, p, nil, nil)
		featureless(t, rr)
	}
	// POST too — scanners sending random POSTs must also get the bare 404.
	rr := doReq(t, h, http.MethodPost, "/"+testSecret+"x/login", url.Values{"username": {"root"}, "password": {"x"}}, nil)
	featureless(t, rr)
}

func TestPanelRoutesBehindPrefix(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	prefix := "/" + testSecret

	// Prefix root requires auth: redirects to the prefixed login page.
	rr := doReq(t, h, http.MethodGet, prefix, nil, nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("GET %s = %d, want redirect to login", prefix, rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != prefix+"/login" {
		t.Fatalf("Location = %q, want %q", loc, prefix+"/login")
	}
	// /login serves the login page.
	rr = doReq(t, h, http.MethodGet, prefix+"/login", nil, nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "VPS Manager") {
		t.Fatalf("GET %s/login = %d, want login page", prefix, rr.Code)
	}
	// Login form posts to the prefixed action.
	if !strings.Contains(rr.Body.String(), prefix+"/login") {
		t.Fatalf("login form does not use prefixed action: %s", rr.Body.String())
	}
}

func TestLoginFlowAndCookiePath(t *testing.T) {
	srv, d := newTestServer(t)
	h := srv.Handler()
	prefix := "/" + testSecret

	hash, err := pw.Hash("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("alice", hash, "10.42.0.2", 1, 10000, 1, 1024, 10); err != nil {
		t.Fatal(err)
	}

	// Wrong password: login page re-rendered, no redirect.
	rr := doReq(t, h, http.MethodPost, prefix+"/login", url.Values{"username": {"alice"}, "password": {"nope"}}, nil)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "invalid credentials") {
		t.Fatalf("bad login: code=%d body=%s", rr.Code, rr.Body.String())
	}

	// Correct password: 302 to the prefix root and cookie scoped to prefix.
	rr = doReq(t, h, http.MethodPost, prefix+"/login", url.Values{"username": {"alice"}, "password": {"correct-horse-battery"}}, nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("login = %d, want 302", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != prefix {
		t.Fatalf("Location = %q, want %q", loc, prefix)
	}
	var cookie *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == "vpsmgr_session" {
			cookie = c
		}
	}
	if cookie == nil {
		t.Fatal("no session cookie set")
	}
	if cookie.Path != prefix {
		t.Fatalf("cookie Path = %q, want %q", cookie.Path, prefix)
	}

	// With the cookie, the prefixed root renders the overview page.
	rr = doReq(t, h, http.MethodGet, prefix, nil, cookie)
	if rr.Code != http.StatusOK || !strings.Contains(rr.Body.String(), "alice") {
		t.Fatalf("GET %s with session = %d, want overview", prefix, rr.Code)
	}
}

func TestUnknownSubpathUnderPrefix(t *testing.T) {
	srv, _ := newTestServer(t)
	h := srv.Handler()
	prefix := "/" + testSecret

	// Unknown routes under the known prefix fall through to the mux: not
	// authenticated, so they redirect to the login page (never a 404 leak).
	rr := doReq(t, h, http.MethodGet, prefix+"/admin", nil, nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("GET %s/admin = %d, want redirect to login", prefix, rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != prefix+"/login" {
		t.Fatalf("Location = %q, want %q", loc, prefix+"/login")
	}
}

func TestOverviewShowsMonthlyTraffic(t *testing.T) {
	srv, _ := newTestServer(t)
	html := srv.renderToString(t, "overview.html", pageData{
		User:   &db.User{Name: "alice"},
		UpGB:   "1.5",
		DownGB: "0.4",
		Prefix: "/" + testSecret,
	})
	for _, want := range []string{"本月流量", "1.5 GB", "0.4 GB", "↑", "↓"} {
		if !strings.Contains(html, want) {
			t.Errorf("overview missing %q", want)
		}
	}
}

// renderToString executes a named template into a string for assertions.
func (s *Server) renderToString(t *testing.T, name string, data pageData) string {
	t.Helper()
	tpl, err := s.templates()
	if err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	if err := tpl.ExecuteTemplate(&b, name, data); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// TestFlashViaAPI verifies result banners are stored server-side and fetched
// via a JSON endpoint (never in the URL), and that the password modal variant
// works the same way.
func TestFlashViaAPI(t *testing.T) {
	srv, d := newTestServer(t)
	h := srv.Handler()
	prefix := "/" + testSecret

	hash, err := pw.Hash("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("alice", hash, "10.42.0.2", 1, 10000, 1, 1024, 10); err != nil {
		t.Fatal(err)
	}

	rr := doReq(t, h, http.MethodPost, prefix+"/login", url.Values{"username": {"alice"}, "password": {"correct-horse-battery"}}, nil)
	var sess *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == "vpsmgr_session" {
			v := *c
			sess = &v
		}
	}
	if sess == nil {
		t.Fatal("no session cookie")
	}

	// A state-changing POST with a mismatched password confirm redirects to the
	// prefix root WITHOUT leaking the message into the URL (deterministic, no
	// lxc involvement).
	rr = doReq(t, h, http.MethodPost, prefix+"/password",
		url.Values{"new_password": {"xxxxxxxxxxxxxxxx"}, "confirm_password": {"yyyyyyyyyyyyyyyy"}}, sess)
	if rr.Code != http.StatusFound {
		t.Fatalf("password = %d, want 302", rr.Code)
	}
	if loc := rr.Header().Get("Location"); loc != prefix {
		t.Fatalf("Location = %q, want %q (message must not be a query param)", loc, prefix)
	}

	// The flash is exposed via /flash as JSON and consumed once.
	rr = doReq(t, h, http.MethodPost, prefix+"/flash", nil, sess)
	if rr.Code != http.StatusOK {
		t.Fatalf("/flash = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "两次输入的密码不一致") {
		t.Fatalf("/flash body = %q, want the stored message", rr.Body.String())
	}
	rr = doReq(t, h, http.MethodPost, prefix+"/flash", nil, sess)
	if !strings.Contains(rr.Body.String(), `"msg":""`) {
		t.Fatalf("/flash after consume = %q, want empty", rr.Body.String())
	}

	// /flash is behind auth.
	rr = doReq(t, h, http.MethodPost, prefix+"/flash", nil, nil)
	if rr.Code != http.StatusFound {
		t.Fatalf("/flash without session = %d, want redirect to login", rr.Code)
	}
}

// TestPasswordModalFlash verifies the modal flash kind (root password / reinstall
// result) is delivered to the frontend.
func TestPasswordModalFlash(t *testing.T) {
	srv, d := newTestServer(t)
	h := srv.Handler()
	prefix := "/" + testSecret

	hash, err := pw.Hash("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := d.CreateUser("alice", hash, "10.42.0.2", 1, 10000, 1, 1024, 10); err != nil {
		t.Fatal(err)
	}

	// Log in to obtain a valid session token, then store a modal flash under it.
	rr := doReq(t, h, http.MethodPost, prefix+"/login", url.Values{"username": {"alice"}, "password": {"correct-horse-battery"}}, nil)
	var sess *http.Cookie
	for _, c := range rr.Result().Cookies() {
		if c.Name == "vpsmgr_session" {
			v := *c
			sess = &v
		}
	}
	if sess == nil {
		t.Fatal("no session cookie")
	}
	srv.flash.Set(sess.Value, "新的 root 密码：\nAbcdefghijk1234567890", "modal")
	req := httptest.NewRequest(http.MethodPost, prefix+"/flash", nil)
	req.AddCookie(sess)
	rw := httptest.NewRecorder()
	h.ServeHTTP(rw, req)
	if rw.Code != http.StatusOK {
		t.Fatalf("/flash = %d, want 200", rw.Code)
	}
	if !strings.Contains(rw.Body.String(), `"kind":"modal"`) {
		t.Fatalf("/flash body = %q, want kind=modal", rw.Body.String())
	}
	if !strings.Contains(rw.Body.String(), "Abcdefghijk1234567890") {
		t.Fatalf("/flash body = %q, want the password", rw.Body.String())
	}
}

func TestStripPrefix(t *testing.T) {
	const prefix = "/Ab1_cdE-9x"
	cases := []struct {
		path, rest string
		ok         bool
	}{
		{prefix, "/", true},
		{prefix + "/", "/", true},
		{prefix + "/login", "/login", true},
		{"/", "", false},
		{prefix + "x", "", false},
		{"//" + prefix, "", false},
	}
	for _, c := range cases {
		rest, ok := stripPrefix(c.path, prefix)
		if ok != c.ok || (ok && rest != c.rest) {
			t.Errorf("stripPrefix(%q) = (%q,%v), want (%q,%v)", c.path, rest, ok, c.rest, c.ok)
		}
	}
}
