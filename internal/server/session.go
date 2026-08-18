// Package authauth: scrypt password hashing + session store for the admin
// backend. The DB-stored hash supersedes the env bootstrap password.
package server

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"sync"
	"time"

	"golang.org/x/crypto/scrypt"
)

// scrypt parameters: 32 MB memory, interactive-appropriate.
const (
	scryptN = 1 << 15
	scryptR = 8
	scryptP = 1
	keyLen  = 32
)

// HashPassword returns salt+hash, both base64, joined by ":".
func HashPassword(pw string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := scrypt.Key([]byte(pw), salt, scryptN, scryptR, scryptP, keyLen)
	if err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(salt) + ":" +
		base64.RawStdEncoding.EncodeToString(key), nil
}

// VerifyPassword checks pw against a stored "salt:hash" (constant time).
func VerifyPassword(pw, stored string) bool {
	saltB64, hashB64, ok := cut(stored, ":")
	if !ok {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(saltB64)
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(hashB64)
	if err != nil {
		return false
	}
	got, err := scrypt.Key([]byte(pw), salt, scryptN, scryptR, scryptP, keyLen)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

func cut(s, sep string) (before, after string, found bool) {
	for i := 0; i+len(sep) <= len(s); i++ {
		if s[i:i+len(sep)] == sep {
			return s[:i], s[i+len(sep):], true
		}
	}
	return s, "", false
}

// Session is an authenticated admin session.
type Session struct {
	Expires time.Time
}

// SessionStore holds sessions in memory. Single-process service; sessions
// are lost on restart, which is the correct fail-closed behavior.
type SessionStore struct {
	mu       sync.Mutex
	sessions map[string]*Session
	ttl      time.Duration
}

func NewSessionStore(ttl time.Duration) *SessionStore {
	return &SessionStore{sessions: map[string]*Session{}, ttl: ttl}
}

// Create returns a new session token.
func (s *SessionStore) Create() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic("entropy source failed")
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	s.mu.Lock()
	// opportunistic sweep
	now := time.Now()
	for k, v := range s.sessions {
		if now.After(v.Expires) {
			delete(s.sessions, k)
		}
	}
	s.sessions[tok] = &Session{Expires: now.Add(s.ttl)}
	s.mu.Unlock()
	return tok
}

// Valid reports whether the token maps to an unexpired session.
func (s *SessionStore) Valid(tok string) bool {
	if tok == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sess, ok := s.sessions[tok]
	if !ok || time.Now().After(sess.Expires) {
		return false
	}
	return true
}

// Revoke removes a session.
func (s *SessionStore) Revoke(tok string) {
	s.mu.Lock()
	delete(s.sessions, tok)
	s.mu.Unlock()
}
