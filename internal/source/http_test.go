package source

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// serveJPEG returns a server always answering with a valid small JPEG.
func serveJPEG(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(smallJPEG(t))
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts
}

func smallJPEG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 200, G: 100, B: 50, A: 255})
		}
	}
	return encodeJPEG(t, img)
}

func encodeJPEG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var buf struct{ b []byte }
	_ = buf
	pr, pw, _ := os.Pipe()
	go func() { jpeg.Encode(pw, img, nil); pw.Close() }()
	data := make([]byte, 0, 32<<10)
	tmp := make([]byte, 4096)
	for {
		n, err := pr.Read(tmp)
		data = append(data, tmp[:n]...)
		if err != nil {
			break
		}
	}
	return data
}

func TestFetchHappyPath(t *testing.T) {
	ts := serveJPEG(t)
	h, err := NewHTTP(HTTPConfig{ID: "c1", Name: "cam", URL: ts.URL, MinInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	f, err := h.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.Width != 320 || f.Height != 240 {
		t.Fatalf("dims %dx%d", f.Width, f.Height)
	}
	if f.JPEG[0] != 0xFF || f.JPEG[1] != 0xD8 {
		t.Fatal("not jpeg")
	}
	if len(f.SHA256) != 32 {
		t.Fatal("sha256 not computed")
	}
}

func TestFetchRejectsBadURLs(t *testing.T) {
	cases := []struct {
		name string
		url  string
	}{
		{"ftp scheme", "ftp://example.com/cam.jpg"},
		{"relative", "/cam.jpg"},
		{"creds in url", "http://user:pass@example.com/cam.jpg"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := NewHTTP(HTTPConfig{ID: "c", URL: c.url}); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestFetchRejectsDenylistedHeaders(t *testing.T) {
	for _, h := range []string{"Host", "Authorization", "Content-Length", "Connection", "Transfer-Encoding"} {
		if _, err := NewHTTP(HTTPConfig{ID: "c", URL: "http://x.example/c.jpg", Headers: map[string]string{h: "v"}}); err == nil {
			t.Fatalf("header %s accepted", h)
		}
	}
}

func TestFetchNonJPEGRejected(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("this is not an image"))
	}))
	defer ts.Close()
	h, _ := NewHTTP(HTTPConfig{ID: "c", URL: ts.URL, MinInterval: time.Millisecond})
	if _, err := h.Fetch(context.Background()); err == nil {
		t.Fatal("non-jpeg accepted")
	}
}

func TestFetchTruncatedJPEGRejected(t *testing.T) {
	jpg := smallJPEG(t)
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(jpg[:len(jpg)/2]) // truncated
	}))
	defer ts.Close()
	h, _ := NewHTTP(HTTPConfig{ID: "c", URL: ts.URL, MinInterval: time.Millisecond})
	if _, err := h.Fetch(context.Background()); err == nil {
		t.Fatal("truncated jpeg accepted")
	}
}

func TestFetchOversizeRejected(t *testing.T) {
	// Frame larger than 4096x4096 cap: generate a wide image config.
	img := image.NewRGBA(image.Rect(0, 0, 5000, 100))
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(encodeJPEG(t, img))
	}))
	defer ts.Close()
	h, _ := NewHTTP(HTTPConfig{ID: "c", URL: ts.URL, MinInterval: time.Millisecond})
	if _, err := h.Fetch(context.Background()); err == nil {
		t.Fatal("oversize accepted")
	}
}

func TestFetchBodyCap(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write([]byte{0xFF, 0xD8})
		chunk := make([]byte, 1<<20)
		for i := 0; i < 9; i++ { // 9 MiB > 8 MiB cap
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	}))
	defer ts.Close()
	h, _ := NewHTTP(HTTPConfig{ID: "c", URL: ts.URL, MinInterval: time.Millisecond, Timeout: 20 * time.Second})
	_, err := h.Fetch(context.Background())
	if err == nil {
		t.Fatal("body over cap accepted")
	}
}

func TestFetchStatusErrors(t *testing.T) {
	for _, code := range []int{404, 500, 403} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		h, _ := NewHTTP(HTTPConfig{ID: "c", URL: ts.URL, MinInterval: time.Millisecond})
		_, err := h.Fetch(context.Background())
		var se *httpStatusError
		if !errors.As(err, &se) || se.Code != code {
			t.Fatalf("code %d: err = %v", code, err)
		}
		ts.Close()
	}
}

func TestRedirectLimit(t *testing.T) {
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Always redirect: 4 hops > 3 allowed.
		http.Redirect(w, r, ts.URL+"/hop", http.StatusFound)
	}))
	defer ts.Close()
	h, _ := NewHTTP(HTTPConfig{ID: "c", URL: ts.URL, MinInterval: time.Millisecond})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := h.Fetch(ctx); err == nil {
		t.Fatal("redirect chain accepted")
	}
}

func TestMetadataRangeBlocked(t *testing.T) {
	h, err := NewHTTP(HTTPConfig{ID: "c", URL: "http://169.254.169.254/latest/meta-data/", MinInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.Fetch(context.Background()); err == nil {
		t.Fatal("metadata address allowed")
	}
}

func TestCredentialRef(t *testing.T) {
	var gotAuth string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if gotAuth == "" {
			w.WriteHeader(401)
			return
		}
		w.Write(smallJPEG(t))
	}))
	defer ts.Close()

	h, err := NewHTTP(HTTPConfig{ID: "c", URL: ts.URL, CredentialRef: "WESTWARD_TEST_CAM", MinInterval: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	// Unset ref -> error, no network dependency on env order.
	t.Setenv("WESTWARD_TEST_CAM", "")
	if _, err := h.Fetch(context.Background()); err == nil {
		t.Fatal("missing env accepted")
	}
	t.Setenv("WESTWARD_TEST_CAM", "user:pass")
	if _, err := h.Fetch(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
	if gotAuth != want {
		t.Fatalf("auth = %q", gotAuth)
	}
}

func TestLimiterSharedPoliteness(t *testing.T) {
	h, _ := NewHTTP(HTTPConfig{ID: "c", URL: "http://x.example/c.jpg", MinInterval: 200 * time.Millisecond})
	ctx := context.Background()
	start := time.Now()
	if err := h.lim.Wait(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	// Second wait must delay ~200 ms.
	if err := h.lim.Wait(ctx, time.Now()); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 150*time.Millisecond {
		t.Fatalf("limiter did not delay: %v", elapsed)
	}
}

func TestNYCTMCListContract(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "testdata", "providers", "nyctmc_list.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list []CameraInfo
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) == 0 {
		t.Fatal("empty fixture")
	}
	for _, c := range list {
		if c.ID == "" || c.Name == "" || c.ImageURL == "" {
			t.Fatalf("incomplete entry: %+v", c)
		}
		if c.IsOnline != "true" && c.IsOnline != "false" {
			t.Fatalf("isOnline not a string bool: %q", c.IsOnline)
		}
	}
}

func TestNYCTMCTwo404Stale(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(404)
	}))
	defer ts.Close()
	n := NewNYCTMC("cam1", "test", "abc")
	n.h.cfg.URL = ts.URL
	n.h.lim.min = time.Millisecond
	ctx := context.Background()
	// First 404: plain error.
	if _, err := n.Fetch(ctx); !errors.Is(err, ErrStale) {
		t.Logf("first 404 err=%v (not stale yet, correct)", err)
	}
	// Second 404: stale.
	if _, err := n.Fetch(ctx); !errors.Is(err, ErrStale) {
		t.Fatalf("second 404: %v, want ErrStale", err)
	}
}

func TestNYCTMCRateLimitBackoff(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(429)
	}))
	defer ts.Close()
	n := NewNYCTMC("cam2", "test", "abc")
	n.h.cfg.URL = ts.URL
	n.h.lim.min = time.Millisecond
	if _, err := n.Fetch(context.Background()); err == nil {
		t.Fatal("429 accepted")
	}
	// Immediate second fetch must be rejected by backoff (fast, no sleep).
	start := time.Now()
	if _, err := n.Fetch(context.Background()); err == nil {
		t.Fatal("backoff not applied")
	}
	if time.Since(start) > time.Second {
		t.Fatal("backoff check slept instead of failing fast")
	}
}

var _ = fmt.Sprintf
var _ = sync.Mutex{}
