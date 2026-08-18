// Package server wires the public site, admin backend, and health endpoints
// behind shared middleware: security headers, Basic auth, CSRF, rate limits.
package server

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/davidtorcivia/westward/internal/health"
)

const (
	maxBodyBytes  = 1 << 20 // 1 MiB admin mutation cap
	csrfCookie    = "westward_csrf"
	sessionCookie = "westward_session"
)

// Auth holds admin credentials and the auth-failure rate limiter.
// Verify is pluggable: the DB scrypt hash when set, else the env bootstrap
// digest. Session cookies are accepted alongside Basic Auth.
type Auth struct {
	User     string
	PWDigest [32]byte                   // sha256 of the env bootstrap password
	Verify   func(password string) bool // supersedes PWDigest when set
	Sessions *SessionStore

	mu    sync.Mutex
	fails map[string][]time.Time // ip -> recent failure timestamps
}

func NewAuth(user, password string) *Auth {
	a := &Auth{User: user, fails: map[string][]time.Time{}, Sessions: NewSessionStore(24 * time.Hour)}
	a.PWDigest = sha256.Sum256([]byte(password))
	return a
}

// Check reports whether user/password are valid. Constant-time on both paths.
func (a *Auth) Check(user, password string) bool {
	userOK := subtle.ConstantTimeCompare([]byte(user), []byte(a.User)) == 1
	var pwOK bool
	if a.Verify != nil {
		pwOK = a.Verify(password)
	} else {
		d := sha256.Sum256([]byte(password))
		pwOK = subtle.ConstantTimeCompare(d[:], a.PWDigest[:]) == 1
	}
	return userOK && pwOK
}

// HasSession reports a valid admin session cookie.
func (a *Auth) HasSession(r *http.Request) bool {
	if c, err := r.Cookie(sessionCookie); err == nil {
		return a.Sessions.Valid(c.Value)
	}
	return false
}

// RecordFailure notes an auth failure for ip; TooManyFailures reports whether
// ip has exceeded 5 failures in the trailing minute.
func (a *Auth) RecordFailure(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	f := a.fails[ip][:0]
	for _, t := range a.fails[ip] {
		if now.Sub(t) < time.Minute {
			f = append(f, t)
		}
	}
	a.fails[ip] = append(f, now)
}

func (a *Auth) TooManyFailures(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	n := 0
	for _, t := range a.fails[ip] {
		if now.Sub(t) < time.Minute {
			n++
		}
	}
	return n > 5
}

// Server is the root HTTP handler.
type Server struct {
	Auth      *Auth
	Log       *slog.Logger
	Heartbeat *health.Heartbeat
	Ready     func() error // nil = ready; checked by /readyz
	Mux       *http.ServeMux
}

func New(auth *Auth, log *slog.Logger, hb *health.Heartbeat) *Server {
	s := &Server{Auth: auth, Log: log, Heartbeat: hb, Mux: http.NewServeMux()}
	hb.Beat()
	s.Mux.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) {
		if hb.Healthy(60 * time.Second) {
			w.WriteHeader(http.StatusOK)
			w.Write([]byte("ok"))
		} else {
			http.Error(w, "stale heartbeat", http.StatusServiceUnavailable)
		}
	})
	s.Mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if s.Ready != nil {
			if err := s.Ready(); err != nil {
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	return s
}

// ServeHTTP applies security headers and dispatches.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	csp := "default-src 'none'; img-src 'self'; style-src 'self'; script-src 'self'"
	// Admin map tiles come from OSM; everything else stays strict.
	if strings.HasPrefix(r.URL.Path, "/admin") {
		csp = "default-src 'none'; img-src 'self' https://tile.openstreetmap.org; " +
			"style-src 'self'; script-src 'self'; connect-src 'self'"
	}
	w.Header().Set("Content-Security-Policy", csp)
	s.Mux.ServeHTTP(w, r)
}

// wantsHTML: browser GET navigations get the login page; curl gets 401.
func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html") &&
		r.Method == http.MethodGet && !strings.HasPrefix(r.URL.Path, "/admin/static")
}

// Admin wraps an admin handler with auth, CSRF, and no-store semantics.
// GETs need auth only; every non-GET also requires a valid CSRF token
// (double-submit: westward_csrf cookie + csrf form field must match).
func (s *Server) Admin(pattern string, h http.HandlerFunc) {
	s.Mux.Handle(pattern, s.adminGuard(h))
}

func (s *Server) adminGuard(h http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)
		if s.Auth.TooManyFailures(ip) {
			http.Error(w, "too many attempts", http.StatusTooManyRequests)
			return
		}
		if !s.Auth.HasSession(r) {
			user, pass, ok := r.BasicAuth()
			if !ok || !s.Auth.Check(user, pass) {
				s.Auth.RecordFailure(ip)
				// Browser navigations get the login page; API/curl gets 401.
				if wantsHTML(r) {
					http.Redirect(w, r, "/admin/login", http.StatusSeeOther)
					return
				}
				w.Header().Set("WWW-Authenticate", `Basic realm="westward admin"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
			if err := r.ParseForm(); err != nil {
				http.Error(w, "bad form", http.StatusBadRequest)
				return
			}
			cookie, err := r.Cookie(csrfCookie)
			token := r.PostFormValue("csrf")
			if err != nil || len(token) != 64 ||
				subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(token)) != 1 {
				http.Error(w, "csrf check failed", http.StatusForbidden)
				return
			}
		}
		h(w, r)
	})
}

// NewCSRFToken returns a 64-hex-char random token.
func NewCSRFToken() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("entropy source failed: %v", err))
	}
	return hex.EncodeToString(b[:])
}

// EnsureCSRFCookie sets the CSRF cookie if absent. Call on every admin GET.
func EnsureCSRFCookie(w http.ResponseWriter, r *http.Request) string {
	if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) == 64 {
		return c.Value
	}
	token := NewCSRFToken()
	http.SetCookie(w, &http.Cookie{
		Name:     csrfCookie,
		Value:    token,
		Path:     "/admin",
		MaxAge:   86400,
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})
	return token
}

// IP extracts the direct client IP for rate limiting (no CF-Connecting-IP
// trust; the tunnel talks to localhost anyway).
func IP(r *http.Request) string {
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return strings.TrimSpace(r.RemoteAddr)
	}
	return ip
}
