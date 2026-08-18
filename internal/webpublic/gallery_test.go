package webpublic

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/davidtorcivia/westward/internal/store"
)

func setup(t *testing.T, days func(st *store.Store, root string)) (*Gallery, *httptest.Server) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	days(st, dir)
	g, err := New(st, dir, func(string) string { return "NYC DOT" })
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	g.Register(mux)
	staticMux := http.NewServeMux()
	staticMux.Handle("GET /static/", Static())
	_ = staticMux
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return g, ts
}

func mkBest(t *testing.T, root, date, hash string, score float64) {
	t.Helper()
	p := filepath.Join(root, "best", date+"."+hash+".jpg")
	os.MkdirAll(filepath.Dir(p), 0o755)
	os.WriteFile(p, []byte{0xFF, 0xD8, 0xFF, 0xD9}, 0o644)
}

func TestGridRenders(t *testing.T) {
	_, ts := setup(t, func(st *store.Store, root string) {
		st.InsertCamera(&store.Camera{ID: "cam", Name: "cam", Type: "httpjpeg", Ref: "http://x/c.jpg", State: "ok", Role: "trigger_only", ThresholdAbs: 12})
		mkBest(t, root, "2026-08-18", "aaaaaaaa", 47.12)
		st.UpsertDayStart("2026-08-18")
		st.UpsertDayStart("2026-08-17")
		st.CompleteDay("2026-08-18", "complete", "", &[]float64{47.12}[0], "cam", 0,
			filepath.Join(root, "best", "2026-08-18.aaaaaaaa.jpg"),
			filepath.Join(root, "best", "thumb", "2026-08-18.aaaaaaaa.480.jpg"),
			filepath.Join(root, "best", "thumb", "2026-08-18.aaaaaaaa.240.jpg"))
		st.CompleteDay("2026-08-17", "missed", "no capture", nil, "", 0, "", "", "")
	})
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.Header.Get("Cache-Control") != "max-age=300" {
		t.Errorf("html cache-control = %q", resp.Header.Get("Cache-Control"))
	}
	buf := new(strings.Builder)
	io.Copy(buf, resp.Body)
	page := buf.String()
	for _, want := range []string{
		`alt="Sunset 2026-08-18, score 47.1"`,
		"2026-08-17 · no capture",
		`srcset="/img/best/thumb/2026-08-18.aaaaaaaa.240.jpg 240w`,
		`data-best="/img/best/2026-08-18.aaaaaaaa.jpg"`,
		`class="vh"`,
	} {
		if !contains(page, want) {
			t.Errorf("grid missing %q", want)
		}
	}
}

func TestImageImmutableAndSafe(t *testing.T) {
	_, ts := setup(t, func(st *store.Store, root string) {
		mkBest(t, root, "2026-08-18", "deadbeef", 50)
	})
	// Valid hashed URL: immutable cache.
	resp, err := http.Get(ts.URL + "/img/best/2026-08-18.deadbeef.jpg")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("valid image = %d", resp.StatusCode)
	}
	if cc := resp.Header.Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("image cache-control = %q", cc)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("content-type = %q", ct)
	}

	// Traversal and junk rejected.
	for _, bad := range []string{
		"/img/best/../../etc/passwd",
		"/img/best/2026-08-18.nohex!.jpg",
		"/img/best/sub/2026-08-18.deadbeef.jpg",
		"/img/best/2026-08-18.deadbeef.json",
	} {
		resp, err := http.Get(ts.URL + bad)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", bad, resp.StatusCode)
		}
	}
}

func TestEmptyGallery(t *testing.T) {
	_, ts := setup(t, func(*store.Store, string) {})
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("empty gallery = %d", resp.StatusCode)
	}
}

func TestPaginationParam(t *testing.T) {
	_, ts := setup(t, func(*store.Store, string) {})
	resp, err := http.Get(ts.URL + "/older?page=2")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("page 2 = %d", resp.StatusCode)
	}
	_ = time.Now
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
