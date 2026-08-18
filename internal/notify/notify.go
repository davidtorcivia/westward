// Package notify implements alert delivery: ntfy, Pushover, and signed
// webhooks. Delivery is durable: events and per-notifier deliveries are
// persisted before any network I/O; a worker fans out with bounded
// concurrency and retries transient failures only.
package notify

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"time"

	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"

	"github.com/davidtorcivia/westward/internal/secutil"
)

// Priority per plan §5: per-notifier mapping.
type Priority uint8

const (
	PriorityNormal Priority = iota
	PriorityHigh
)

// Alert is one outbound event.
type Alert struct {
	EventID   string
	Kind      string // headsup|go|test|system
	Title     string
	Body      string
	ImageJPEG []byte // optional
	ClickURL  string
	Priority  Priority
}

// Notifier delivers alerts.
type Notifier interface {
	Name() string
	Send(ctx context.Context, a Alert) error
}

// Error classification: Transient failures are retried; Permanent never are.
type TransientError struct{ Err error }

func (e *TransientError) Error() string { return "transient: " + e.Err.Error() }
func (e *TransientError) Unwrap() error { return e.Err }

type PermanentError struct{ Err error }

func (e *PermanentError) Error() string { return "permanent: " + e.Err.Error() }
func (e *PermanentError) Unwrap() error { return e.Err }

// classify maps transport/HTTP outcomes to retry decisions.
func classify(err error, status int) error {
	if err != nil {
		return &TransientError{Err: err}
	}
	if status >= 500 || status == http.StatusTooManyRequests {
		return &TransientError{Err: fmt.Errorf("status %d", status)}
	}
	if status >= 400 {
		return &PermanentError{Err: fmt.Errorf("status %d", status)}
	}
	return nil
}

const clientTimeout = 10 * time.Second

// Ntfy delivers via the ntfy HTTP PUT API.
type Ntfy struct {
	Server   string
	Topic    string
	TokenEnv string // optional access token env name
}

func (n *Ntfy) Name() string { return "ntfy" }

func (n *Ntfy) priority(p Priority) string {
	if p == PriorityHigh {
		return "4"
	}
	return "3"
}

func (n *Ntfy) Send(ctx context.Context, a Alert) error {
	url := n.Server + "/" + n.Topic
	priorityHdr := map[Priority]string{PriorityNormal: "3", PriorityHigh: "4"}

	send := func(withImage bool) error {
		var body io.Reader
		if withImage && len(a.ImageJPEG) > 0 {
			body = bytes.NewReader(a.ImageJPEG)
		} else {
			body = bytes.NewReader([]byte(a.Body))
		}
		req, err := http.NewRequestWithContext(ctx, "PUT", url, body)
		if err != nil {
			return &PermanentError{Err: err}
		}
		req.Header.Set("X-Title", a.Title)
		if withImage && len(a.ImageJPEG) > 0 {
			req.Header.Set("X-Message", a.Body)
			req.Header.Set("X-Filename", "sunset.jpg")
			req.Header.Set("Content-Type", "image/jpeg")
		}
		req.Header.Set("X-Priority", priorityHdr[a.Priority])
		if a.ClickURL != "" {
			req.Header.Set("X-Click", a.ClickURL)
		}
		if n.TokenEnv != "" {
			if tok := os.Getenv(n.TokenEnv); tok != "" {
				req.Header.Set("Authorization", "Bearer "+tok)
			}
		}
		resp, err := (&http.Client{Timeout: clientTimeout}).Do(req)
		if err != nil {
			return &TransientError{Err: redactErr(err)}
		}
		defer resp.Body.Close()
		io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
		return classify(nil, resp.StatusCode)
	}

	if len(a.ImageJPEG) > 0 {
		if err := send(true); err == nil {
			return nil
		} else if _, ok := err.(*PermanentError); !ok {
			// attachment failed transiently: retry once text-only
			return send(false)
		} else {
			return err
		}
	}
	return send(false)
}

// Pushover delivers via multipart POST. Attachments re-encode down from
// 1280px q80 through a ladder until under the 5 MiB cap.
type Pushover struct {
	TokenEnv string // app token env
	UserEnv  string // user key env
	// Encode is the re-encode step (injected: engine provides jpeg scaling).
	Encode        func(jpegBytes []byte, maxW int, quality int) ([]byte, error)
	MaxAttachment int    // default 5 MiB
	BaseURL       string // override for tests; default api.pushover.net
}

func (p *Pushover) Name() string { return "pushover" }

func (p *Pushover) priority(pr Priority) string {
	if pr == PriorityHigh {
		return "1"
	}
	return "0"
}

var pushoverLadder = []struct {
	Width   int
	Quality int
}{
	{1280, 80}, {1280, 70}, {960, 70}, {720, 65},
}

func (p *Pushover) Send(ctx context.Context, a Alert) error {
	token, user := os.Getenv(p.TokenEnv), os.Getenv(p.UserEnv)
	if token == "" || user == "" {
		return &PermanentError{Err: fmt.Errorf("pushover: %s/%s unset", p.TokenEnv, p.UserEnv)}
	}
	cap := p.MaxAttachment
	if cap == 0 {
		cap = 5 << 20
	}

	var attachment []byte
	if len(a.ImageJPEG) > 0 {
		if len(a.ImageJPEG) <= cap {
			attachment = a.ImageJPEG
		} else if p.Encode != nil {
			for _, step := range pushoverLadder {
				enc, err := p.Encode(a.ImageJPEG, step.Width, step.Quality)
				if err != nil {
					continue
				}
				if len(enc) <= cap {
					attachment = enc
					break
				}
			}
		}
		if attachment == nil {
			return &PermanentError{Err: fmt.Errorf("pushover: image cannot fit %d bytes", cap)}
		}
	}

	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	mw.WriteField("token", token)
	mw.WriteField("user", user)
	mw.WriteField("title", a.Title)
	mw.WriteField("message", a.Body)
	mw.WriteField("priority", p.priority(a.Priority))
	if a.ClickURL != "" {
		mw.WriteField("url", a.ClickURL)
	}
	if attachment != nil {
		fw, _ := mw.CreateFormFile("attachment", "sunset.jpg")
		fw.Write(attachment)
	}
	mw.Close()

	base := p.BaseURL
	if base == "" {
		base = "https://api.pushover.net"
	}
	req, err := http.NewRequestWithContext(ctx, "POST", base+"/1/messages.json", &buf)
	if err != nil {
		return &PermanentError{Err: err}
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := (&http.Client{Timeout: clientTimeout}).Do(req)
	if err != nil {
		return &TransientError{Err: redactErr(err)}
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return classify(nil, resp.StatusCode)
}

// Webhook delivers a signed JSON POST. Receivers dedupe on event_id.
type Webhook struct {
	URL     string
	HMACEnv string // optional; hex hmac-sha256 of the exact body
}

func (w *Webhook) Name() string { return "webhook" }

func (w *Webhook) Send(ctx context.Context, a Alert) error {
	payload := map[string]any{
		"event_id":  a.EventID,
		"kind":      a.Kind,
		"title":     a.Title,
		"body":      a.Body,
		"click_url": a.ClickURL,
		"ts_ms":     time.Now().UnixMilli(),
		"priority":  map[Priority]string{PriorityNormal: "Normal", PriorityHigh: "High"}[a.Priority],
	}
	if len(a.ImageJPEG) > 0 {
		payload["image_b64"] = base64.StdEncoding.EncodeToString(a.ImageJPEG)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return &PermanentError{Err: err}
	}
	req, err := http.NewRequestWithContext(ctx, "POST", w.URL, bytes.NewReader(body))
	if err != nil {
		return &PermanentError{Err: err}
	}
	req.Header.Set("Content-Type", "application/json")
	if w.HMACEnv != "" {
		if key := os.Getenv(w.HMACEnv); key != "" {
			mac := hmac.New(sha256.New, []byte(key))
			mac.Write(body)
			req.Header.Set("X-Westward-Signature", hex.EncodeToString(mac.Sum(nil)))
		}
	}
	resp, err := (&http.Client{Timeout: clientTimeout}).Do(req)
	if err != nil {
		return &TransientError{Err: redactErr(err)}
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	return classify(nil, resp.StatusCode)
}

// redactErr scrubs URLs/tokens before an error reaches logs or the DB.
func redactErr(err error) error {
	return fmt.Errorf("%s", secutil.Redact(err.Error()))
}

// Build instantiates notifiers from static config definitions.
func Build(defs []struct {
	ID, Type                                       string
	Server, Topic, TokenEnv, UserEnv, URL, HMACEnv string
}) map[string]Notifier {
	out := map[string]Notifier{}
	for _, d := range defs {
		switch d.Type {
		case "ntfy":
			out[d.ID] = &Ntfy{Server: d.Server, Topic: d.Topic, TokenEnv: d.TokenEnv}
		case "pushover":
			out[d.ID] = &Pushover{TokenEnv: d.TokenEnv, UserEnv: d.UserEnv}
		case "webhook":
			out[d.ID] = &Webhook{URL: d.URL, HMACEnv: d.HMACEnv}
		}
	}
	return out
}
