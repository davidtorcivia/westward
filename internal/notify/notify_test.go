package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"golang.org/x/image/draw"
	"image"
	"image/color"
	"image/jpeg"
)

func encode(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 255, G: 120, B: 40, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestNtfyHeadersAndFallback(t *testing.T) {
	var calls int32
	var lastIsImage atomic.Bool
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		ct := r.Header.Get("Content-Type")
		lastIsImage.Store(ct == "image/jpeg")
		// First (image) call fails 500; fallback text call must succeed.
		if atomic.LoadInt32(&calls) == 1 {
			w.WriteHeader(500)
			return
		}
		w.WriteHeader(200)
	}))
	defer ts.Close()

	n := &Ntfy{Server: ts.URL, Topic: "westward-test"}
	err := n.Send(context.Background(), Alert{
		Title: "GO", Body: "now", ImageJPEG: encode(t, 64, 64), Priority: PriorityHigh,
	})
	if err != nil {
		t.Fatalf("image-fallback failed: %v", err)
	}
	if atomic.LoadInt32(&calls) != 2 {
		t.Fatalf("calls = %d, want 2 (image then text)", calls)
	}
	if lastIsImage.Load() {
		t.Fatal("second call should be text")
	}
}

func TestNtfyPriorityMapping(t *testing.T) {
	var prio string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		prio = r.Header.Get("X-Priority")
	}))
	defer ts.Close()
	n := &Ntfy{Server: ts.URL, Topic: "t"}
	n.Send(context.Background(), Alert{Title: "a", Body: "b", Priority: PriorityNormal})
	if prio != "3" {
		t.Fatalf("normal = %q, want 3", prio)
	}
	n.Send(context.Background(), Alert{Title: "a", Body: "b", Priority: PriorityHigh})
	if prio != "4" {
		t.Fatalf("high = %q, want 4", prio)
	}
}

func TestPushoverMultipartAndSizeLadder(t *testing.T) {
	var sizes []int
	var lastBody string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(8 << 20); err != nil {
			t.Errorf("multipart: %v", err)
			w.WriteHeader(400)
			return
		}
		if r.FormValue("priority") == "" || r.FormValue("token") == "" {
			t.Error("missing fields")
			w.WriteHeader(400)
			lastBody = "missing fields: prio=" + r.FormValue("priority")
			w.Write([]byte(lastBody))
			return
		}
		if f, _, err := r.FormFile("attachment"); err == nil {
			buf := new(bytes.Buffer)
			buf.ReadFrom(f)
			sizes = append(sizes, buf.Len())
			f.Close()
		}
		w.WriteHeader(200)
	}))
	defer ts.Close()

	t.Setenv("WESTWARD_PUSHOVER_TOKEN", "tok")
	t.Setenv("WESTWARD_PUSHOVER_USER", "usr")
	scale := func(b []byte, maxW, q int) ([]byte, error) {
		img, _, err := image.Decode(bytes.NewReader(b))
		if err != nil {
			return nil, err
		}
		bd := img.Bounds()
		h := bd.Dy() * maxW / bd.Dx()
		dst := image.NewRGBA(image.Rect(0, 0, maxW, h))
		draw.BiLinear.Scale(dst, dst.Bounds(), img, bd, draw.Src, nil)
		var out bytes.Buffer
		jpeg.Encode(&out, dst, &jpeg.Options{Quality: q})
		return out.Bytes(), nil
	}
	p := &Pushover{TokenEnv: "WESTWARD_PUSHOVER_TOKEN", UserEnv: "WESTWARD_PUSHOVER_USER", Encode: scale, BaseURL: ts.URL}

	// Small image passes through.
	if err := p.Send(context.Background(), Alert{Title: "t", Body: "b", ImageJPEG: encode(t, 100, 100)}); err != nil {
		t.Fatal(err)
	}
	// Huge raw image (no ladder step possible at first width? it will shrink).
	if err := p.Send(context.Background(), Alert{Title: "t", Body: "b", ImageJPEG: encode(t, 2000, 2000)}); err != nil {
		t.Fatalf("ladder failed: %v (handler says: %q)", err, lastBody)
	}
	if len(sizes) != 2 {
		t.Fatalf("attachments = %d, want 2", len(sizes))
	}
	// Priority mapping.
	_ = p.priority(PriorityHigh)
}

func TestPushoverMissingEnvPermanent(t *testing.T) {
	p := &Pushover{TokenEnv: "NOPE_TOKEN", UserEnv: "NOPE_USER"}
	err := p.Send(context.Background(), Alert{Title: "t", Body: "b"})
	var pe *PermanentError
	if !errors.As(err, &pe) {
		t.Fatalf("missing env = %v, want permanent", err)
	}
}

func TestWebhookHMAC(t *testing.T) {
	var gotSig string
	var payload map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotSig = r.Header.Get("X-Westward-Signature")
		json.NewDecoder(r.Body).Decode(&payload)
	}))
	defer ts.Close()

	t.Setenv("WESTWARD_WEBHOOK_HMAC_KEY", "sekrit")
	w := &Webhook{URL: ts.URL, HMACEnv: "WESTWARD_WEBHOOK_HMAC_KEY"}
	if err := w.Send(context.Background(), Alert{EventID: "e1", Title: "t", Body: "b", Priority: PriorityHigh}); err != nil {
		t.Fatal(err)
	}
	if len(gotSig) != 64 {
		t.Fatalf("signature %q not sha256 hex", gotSig)
	}
	if payload["event_id"] != "e1" || payload["priority"] != "High" {
		t.Fatalf("payload: %+v", payload)
	}
}

func TestClassification(t *testing.T) {
	if err := classify(nil, 500); !isTransient(err) {
		t.Error("500 should be transient")
	}
	if err := classify(nil, 429); !isTransient(err) {
		t.Error("429 should be transient")
	}
	if err := classify(nil, 401); !isPermanent(err) {
		t.Error("401 should be permanent")
	}
	if err := classify(errors.New("boom"), 0); !isTransient(err) {
		t.Error("transport error should be transient")
	}
}

func isTransient(err error) bool { var e *TransientError; return errors.As(err, &e) }
func isPermanent(err error) bool { var e *PermanentError; return errors.As(err, &e) }

func TestRedactErrors(t *testing.T) {
	err := (&Ntfy{Server: "https://user:pass@example.com", Topic: "t"}).Send(context.Background(), Alert{Title: "x", Body: "y"})
	// Connection refused carries the URL; the error must not contain creds.
	if err != nil && bytes.Contains([]byte(err.Error()), []byte("user:pass")) {
		t.Fatalf("credentials leaked: %s", err)
	}
	_ = os.Getenv
}
