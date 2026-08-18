package server

import (
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/davidtorcivia/westward/internal/health"
)

func testServer() (*Server, *httptest.Server) {
	s := New(NewAuth("admin", "a-12-char-password"), nil, newHeartbeat())
	// Minimal login route mirroring webadmin's wiring (rendering lives there).
	s.Mux.HandleFunc("POST /admin/login", func(w http.ResponseWriter, r *http.Request) {
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if s.Auth.TooManyFailures(ip) {
			http.Error(w, "slow down", http.StatusTooManyRequests)
			return
		}
		r.ParseForm()
		if !s.Auth.Check(r.PostFormValue("username"), r.PostFormValue("password")) {
			s.Auth.RecordFailure(ip)
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("login form with error"))
			return
		}
		http.SetCookie(w, &http.Cookie{
			Name: "westward_session", Value: s.Auth.Sessions.Create(),
			Path: "/admin", MaxAge: 86400, HttpOnly: true, SameSite: http.SameSiteLaxMode,
		})
		http.Redirect(w, r, "/admin", http.StatusSeeOther)
	})
	s.Mux.HandleFunc("GET /admin/login", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("login form"))
	})
	s.Admin("GET /admin", func(w http.ResponseWriter, r *http.Request) {
		EnsureCSRFCookie(w, r)
		w.Write([]byte("admin ok"))
	})
	s.Admin("POST /admin/thing", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("mutated"))
	})
	ts := httptest.NewServer(s)
	return s, ts
}

func newHeartbeat() *health.Heartbeat {
	hb := &health.Heartbeat{}
	hb.Beat()
	return hb
}

func get(t *testing.T, url string, hdr map[string]string) *http.Response {
	t.Helper()
	req, _ := http.NewRequest("GET", url, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func basic(user, pass string) map[string]string {
	req := httptest.NewRequest("GET", "/", nil)
	req.SetBasicAuth(user, pass)
	// Extract header value set by SetBasicAuth.
	v := req.Header.Get("Authorization")
	return map[string]string{"Authorization": v}
}

func TestSecurityHeaders(t *testing.T) {
	_, ts := testServer()
	defer ts.Close()
	resp := get(t, ts.URL+"/livez", nil)
	resp.Body.Close()
	for k, want := range map[string]string{
		"X-Content-Type-Options":  "nosniff",
		"X-Frame-Options":         "DENY",
		"Content-Security-Policy": "default-src 'none'; img-src 'self'; style-src 'self'; script-src 'self'",
	} {
		if got := resp.Header.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestHealthEndpoints(t *testing.T) {
	_, ts := testServer()
	defer ts.Close()
	resp := get(t, ts.URL+"/livez", nil)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("livez = %d", resp.StatusCode)
	}
	resp = get(t, ts.URL+"/readyz", nil)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Errorf("readyz with nil Ready = %d", resp.StatusCode)
	}
}

func TestReadyzUnhealthy(t *testing.T) {
	s := New(NewAuth("a", "b"), nil, newHeartbeat())
	s.Ready = func() error { return http.ErrServerClosed }
	ts := httptest.NewServer(s)
	defer ts.Close()
	resp := get(t, ts.URL+"/readyz", nil)
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("readyz = %d, want 503", resp.StatusCode)
	}
}

func TestAdminAuth(t *testing.T) {
	_, ts := testServer()
	defer ts.Close()
	cases := []struct {
		name       string
		hdr        map[string]string
		wantStatus int
	}{
		{"no creds", nil, 401},
		{"wrong user", basic("root", "a-12-char-password"), 401},
		{"wrong pass", basic("admin", "wrong-password!"), 401},
		{"ok", basic("admin", "a-12-char-password"), 200},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			resp := get(t, ts.URL+"/admin", c.hdr)
			resp.Body.Close()
			if resp.StatusCode != c.wantStatus {
				t.Errorf("status = %d, want %d", resp.StatusCode, c.wantStatus)
			}
		})
	}
	// Admin responses are no-store.
	resp := get(t, ts.URL+"/admin", basic("admin", "a-12-char-password"))
	resp.Body.Close()
	if cc := resp.Header.Get("Cache-Control"); cc != "no-store" {
		t.Errorf("cache-control = %q", cc)
	}
}

func TestCSRFEnforced(t *testing.T) {
	s, ts := testServer()
	defer ts.Close()
	auth := basic("admin", "a-12-char-password")

	// Get the CSRF cookie from a GET.
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", ts.URL+"/admin", nil)
	for k, v := range auth {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var token string
	for _, c := range resp.Cookies() {
		if c.Name == "westward_csrf" {
			token = c.Value
		}
	}
	if token == "" {
		t.Fatal("no csrf cookie set")
	}

	post := func(form string, cookieToken string) *http.Response {
		t.Helper()
		req, _ := http.NewRequest("POST", ts.URL+"/admin/thing", strings.NewReader(form))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		for k, v := range auth {
			req.Header.Set(k, v)
		}
		if cookieToken != "" {
			req.AddCookie(&http.Cookie{Name: "westward_csrf", Value: cookieToken})
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	if resp := post("csrf=wrong-token-0000000000000000000000000000000000000000000000000000", token); resp.StatusCode != http.StatusForbidden {
		t.Errorf("mismatched form token: %d, want 403", resp.StatusCode)
	}
	if resp := post("csrf="+token, token); resp.StatusCode != 200 {
		t.Errorf("valid token: %d, want 200", resp.StatusCode)
	}
	if resp := post("csrf="+token, "different-cookie-token-000000000000000000000000000"); resp.StatusCode != http.StatusForbidden {
		t.Errorf("mismatched cookie: %d, want 403", resp.StatusCode)
	}
	if resp := post("csrf="+token, ""); resp.StatusCode != http.StatusForbidden {
		t.Errorf("missing cookie: %d, want 403", resp.StatusCode)
	}
	_ = s
}

func TestAuthRateLimit(t *testing.T) {
	_, ts := testServer()
	defer ts.Close()
	// 6 failures in a row -> 429.
	for i := 0; i < 6; i++ {
		resp := get(t, ts.URL+"/admin", basic("admin", "nope-nope-nope"))
		resp.Body.Close()
		if i < 5 && resp.StatusCode != 401 {
			t.Fatalf("attempt %d: %d, want 401", i, resp.StatusCode)
		}
	}
	resp := get(t, ts.URL+"/admin", basic("admin", "nope-nope-nope"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("7th attempt: %d, want 429", resp.StatusCode)
	}
	// Valid creds are also locked out from this IP.
	resp = get(t, ts.URL+"/admin", basic("admin", "a-12-char-password"))
	resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Errorf("valid creds while limited: %d, want 429", resp.StatusCode)
	}
}

func TestMethodRestrictions(t *testing.T) {
	_, ts := testServer()
	defer ts.Close()
	req, _ := http.NewRequest("POST", ts.URL+"/admin", strings.NewReader("x=1"))
	req.SetBasicAuth("admin", "a-12-char-password")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST to GET-only route: %d, want 405", resp.StatusCode)
	}
}

func TestLoginSessionFlow(t *testing.T) {
	s, ts := testServer()
	defer ts.Close()

	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	// GET login page unauthenticated.
	resp, err := client.Get(ts.URL + "/admin/login")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("login page = %d", resp.StatusCode)
	}

	// POST wrong password: no session cookie.
	resp, err = client.PostForm(ts.URL+"/admin/login", map[string][]string{
		"username": {"admin"}, "password": {"wrong-wrong-wrong"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 { // renders the form again with the error
		t.Fatalf("wrong password = %d", resp.StatusCode)
	}

	// POST correct: session cookie issued, admin accessible without Basic Auth.
	resp, err = client.PostForm(ts.URL+"/admin/login", map[string][]string{
		"username": {"admin"}, "password": {"a-12-char-password"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	var tok string
	for _, c := range resp.Cookies() {
		if c.Name == "westward_session" {
			tok = c.Value
		}
	}
	if tok == "" {
		t.Fatal("no session cookie")
	}
	req, _ := http.NewRequest("GET", ts.URL+"/admin", nil)
	req.AddCookie(&http.Cookie{Name: "westward_session", Value: tok})
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("session admin access = %d", resp.StatusCode)
	}

	// Revoke: logout kills it.
	s.Auth.Sessions.Revoke(tok)
	req, _ = http.NewRequest("GET", ts.URL+"/admin", nil)
	req.AddCookie(&http.Cookie{Name: "westward_session", Value: tok})
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatalf("revoked session = %d, want 401 (curl path)", resp.StatusCode)
	}
}

func TestHashVerifyPassword(t *testing.T) {
	h, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyPassword("correct horse battery staple", h) {
		t.Fatal("valid password rejected")
	}
	if VerifyPassword("wrong horse battery staple", h) {
		t.Fatal("wrong password accepted")
	}
	if VerifyPassword("pw", "not-a-valid-hash") {
		t.Fatal("malformed hash accepted")
	}
}

func TestBrowserRedirectToLogin(t *testing.T) {
	_, ts := testServer()
	defer ts.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, _ := http.NewRequest("GET", ts.URL+"/admin", nil)
	req.Header.Set("Accept", "text/html,application/xhtml+xml")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusSeeOther {
		t.Fatalf("browser /admin = %d, want 303 to login", resp.StatusCode)
	}
	if loc := resp.Header.Get("Location"); loc != "/admin/login" {
		t.Fatalf("location = %q", loc)
	}
}
