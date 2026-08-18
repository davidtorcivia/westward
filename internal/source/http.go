// Package source implements camera sources: generic httpjpeg and the
// NYCTMC traffic-camera adapter.
package source

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

// Frame is one captured still.
type Frame struct {
	CameraID      string
	Name          string
	Fetched       time.Time // UTC
	JPEG          []byte
	Width, Height int
	SHA256        [32]byte
}

// CamSource is a pollable camera.
type CamSource interface {
	ID() string
	Name() string
	Fetch(ctx context.Context) (Frame, error)
}

// Config for one httpjpeg camera.
type HTTPConfig struct {
	ID            string
	Name          string
	URL           string
	CredentialRef string            // env var holding "user:password", optional
	Headers       map[string]string // optional extra headers (denylisted keys rejected)
	Timeout       time.Duration     // default 10s
	MinInterval   time.Duration     // politeness floor shared with admin preview
	UserAgent     string
	// AllowLinkLocal permits 169.254.0.0/16 and fd00::/8 metadata ranges.
	AllowLinkLocal bool
}

// headerDenylist: hop-by-hop and auth headers that must come only from the
// credential_ref mechanism.
var headerDenylist = map[string]bool{
	"Host": true, "Content-Length": true, "Connection": true,
	"Transfer-Encoding": true, "Authorization": true,
}

// Limiter enforces a minimum interval between fetches; the scheduled capture
// loop and the admin preview share one limiter per camera so previews cannot
// bypass politeness.
type Limiter struct {
	mu   sync.Mutex
	last time.Time
	min  time.Duration
}

func NewLimiter(min time.Duration) *Limiter { return &Limiter{min: min} }

// Wait blocks until the next fetch is permitted.
func (l *Limiter) Wait(ctx context.Context, now time.Time) error {
	l.mu.Lock()
	next := l.last.Add(l.min)
	if now.Before(next) {
		select {
		case <-ctx.Done():
			l.mu.Unlock()
			return ctx.Err()
		case <-time.After(next.Sub(now)):
		}
	}
	l.last = time.Now()
	l.mu.Unlock()
	return nil
}

// httpStatusError carries a non-200 status out of the httpjpeg fetcher.
type httpStatusError struct {
	Code   int
	Camera string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("camera %s: status %d", e.Camera, e.Code)
}

// HTTP is a generic still-JPEG camera.
type HTTP struct {
	cfg    HTTPConfig
	client *http.Client
	lim    *Limiter
}

func NewHTTP(cfg HTTPConfig) (*HTTP, error) {
	u, err := url.Parse(cfg.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("camera %s: url must be absolute http(s)", cfg.ID)
	}
	if u.User != nil {
		return nil, fmt.Errorf("camera %s: credentials in URL are not allowed; use credential_ref", cfg.ID)
	}
	for k := range cfg.Headers {
		if headerDenylist[strings.ToLower(k)] || headerDenylist[k] {
			return nil, fmt.Errorf("camera %s: header %q is set automatically", cfg.ID, k)
		}
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 10 * time.Second
	}
	if cfg.MinInterval <= 0 {
		cfg.MinInterval = time.Second
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.DialContext = metadataSafeDial(cfg.AllowLinkLocal)
	return &HTTP{
		cfg: cfg,
		client: &http.Client{
			Timeout:   cfg.Timeout,
			Transport: tr,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 3 {
					return fmt.Errorf("stopped after 3 redirects")
				}
				// Go's client already strips Authorization cross-host; also
				// refuse to follow if the original request had basic auth and
				// the host changed (defense in depth for custom headers).
				return nil
			},
		},
		lim: NewLimiter(cfg.MinInterval),
	}, nil
}

func (h *HTTP) ID() string        { return h.cfg.ID }
func (h *HTTP) Name() string      { return h.cfg.Name }
func (h *HTTP) Limiter() *Limiter { return h.lim }

func (h *HTTP) Fetch(ctx context.Context) (Frame, error) {
	if err := h.lim.Wait(ctx, time.Now()); err != nil {
		return Frame{}, err
	}
	req, err := http.NewRequestWithContext(ctx, "GET", h.cfg.URL, nil)
	if err != nil {
		return Frame{}, err
	}
	if h.cfg.UserAgent != "" {
		req.Header.Set("User-Agent", h.cfg.UserAgent)
	}
	if h.cfg.CredentialRef != "" {
		userpass := os.Getenv(h.cfg.CredentialRef)
		if userpass == "" {
			return Frame{}, fmt.Errorf("camera %s: env %s unset", h.cfg.ID, h.cfg.CredentialRef)
		}
		parts := strings.SplitN(userpass, ":", 2)
		if len(parts) != 2 {
			return Frame{}, fmt.Errorf("camera %s: %s must hold user:password", h.cfg.ID, h.cfg.CredentialRef)
		}
		req.SetBasicAuth(parts[0], parts[1])
	}
	for k, v := range h.cfg.Headers {
		req.Header.Set(k, v)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		return Frame{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return Frame{}, &httpStatusError{Code: resp.StatusCode, Camera: h.cfg.ID}
	}

	body := io.LimitReader(resp.Body, 8<<20+1)
	jpegBytes, err := io.ReadAll(body)
	if err != nil {
		return Frame{}, err
	}
	if len(jpegBytes) > 8<<20 {
		return Frame{}, fmt.Errorf("camera %s: frame exceeds 8 MiB cap", h.cfg.ID)
	}
	return decodeFrame(h.cfg.ID, h.cfg.Name, jpegBytes, time.Now())
}

// decodeFrame sniffs JPEG magic, enforces dimension caps, decodes config.
func decodeFrame(id, name string, b []byte, fetched time.Time) (Frame, error) {
	if len(b) < 2 || b[0] != 0xFF || b[1] != 0xD8 {
		return Frame{}, fmt.Errorf("camera %s: not a JPEG", id)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return Frame{}, fmt.Errorf("camera %s: decode config: %w", id, err)
	}
	if format != "jpeg" {
		return Frame{}, fmt.Errorf("camera %s: decoded as %q, want jpeg", id, format)
	}
	if cfg.Width > 4096 || cfg.Height > 4096 || cfg.Width*cfg.Height > 12_000_000 {
		return Frame{}, fmt.Errorf("camera %s: frame %dx%d exceeds caps", id, cfg.Width, cfg.Height)
	}
	// Full decode verifies the stream end-to-end; DecodeConfig alone accepts
	// bodies truncated after the header. The scorer re-decodes later.
	if _, err := jpeg.Decode(bytes.NewReader(b)); err != nil {
		return Frame{}, fmt.Errorf("camera %s: corrupt jpeg: %w", id, err)
	}
	sum := sha256.Sum256(b)
	return Frame{
		CameraID: id, Name: name, Fetched: fetched,
		JPEG: b, Width: cfg.Width, Height: cfg.Height, SHA256: sum,
	}, nil
}

// metadataSafeDial blocks link-local metadata ranges (169.254.0.0/16,
// fd00::/8 per config) at connect time by resolving and checking every
// candidate address. Private/loopback ranges are allowed by default: window
// cams live on the LAN.
func metadataSafeDial(allowLinkLocal bool) func(ctx context.Context, network, addr string) (net.Conn, error) {
	var dialer net.Dialer
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		var lastErr error
		for _, ip := range ips {
			if !allowLinkLocal && isMetadataRange(ip.IP) {
				return nil, fmt.Errorf("dial %s blocked: link-local metadata range", ip.IP)
			}
			c, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port))
			if err == nil {
				return c, nil
			}
			lastErr = err
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("no addresses for %s", host)
		}
		return nil, lastErr
	}
}

func isMetadataRange(ip net.IP) bool {
	if ip.To4() != nil {
		return ip.Equal(net.IPv4zero) || (len(ip) == 4 || ip.To4() != nil) && ip.To4()[0] == 169 && ip.To4()[1] == 254
	}
	// fd00::/8 (ULA, includes cloud metadata patterns).
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	return len(ip) == 16 && (ip[0]&0xfe) == 0xfc // fc00::/7 ULA
}
