// Package secutil holds secret-safe logging, CSRF, and security-header helpers.
package secutil

import (
	"net/url"
	"strings"
)

// Redact strips userinfo from any URL-shaped substring in s. Returns the
// cleaned string; never panics on malformed input. Use on every error or
// URL before it reaches a log line.
func Redact(s string) string {
	// Fast path: nothing that looks credentialed.
	if !strings.Contains(s, "://") && !strings.Contains(s, "@") {
		return s
	}
	var b strings.Builder
	i := 0
	for {
		sch := strings.Index(s[i:], "://")
		if sch < 0 {
			b.WriteString(s[i:])
			return b.String()
		}
		start := i + sch
		rest := s[start+3:]
		// scheme://[userinfo@]host... — cut at the first '/', '?', or space-ish end.
		end := len(rest)
		for j := 0; j < len(rest); j++ {
			c := rest[j]
			if c == '/' || c == '?' || c == '#' || c == ' ' || c == '"' || c == '\'' {
				end = j
				break
			}
		}
		authority := rest[:end]
		if at := strings.LastIndex(authority, "@"); at >= 0 {
			b.WriteString(s[i : start+3])
			b.WriteString("redacted:")
			b.WriteString(authority[at+1:])
			i = start + 3 + end
		} else {
			b.WriteString(s[i : start+3+end])
			i = start + 3 + end
		}
	}
}

// RedactURL redacts a parsed URL's userinfo, returning a display string.
func RedactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	c := *u
	if c.User != nil {
		c.User = url.User("redacted")
	}
	return c.String()
}
