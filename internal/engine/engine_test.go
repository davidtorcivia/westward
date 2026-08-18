package engine

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/davidtorcivia/westward/internal/clock"
	"github.com/davidtorcivia/westward/internal/config"
	"github.com/davidtorcivia/westward/internal/health"
	"github.com/davidtorcivia/westward/internal/solar"
	"github.com/davidtorcivia/westward/internal/store"
)

// scriptedCam returns sequential JPEGs: dim grays then vivid oranges.
type scriptedCam struct {
	mu sync.Mutex
	n  int
	t  *testing.T
}

func newScriptedCam(t *testing.T) *scriptedCam {
	t.Helper()
	return &scriptedCam{t: t}
}

// fetch always returns a NEW image: 6 gray shades, then oranges with a
// cycling green channel. Distinct bytes forever so SHA dedup never stalls
// the sample stream (a real camera always yields fresh frames).
func (s *scriptedCam) fetch(ctx context.Context) ([]byte, int, int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := s.n
	s.n++
	if i < 6 {
		v := uint8(110 + i)
		return encodeSolid(s.t, v, v, v), 320, 240, nil
	}
	return encodeSolid(s.t, 255, uint8(110+i%140), 40), 320, 240, nil
}

func encodeSolid(t *testing.T, r, g, b uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 320, 240))
	for y := 0; y < 240; y++ {
		for x := 0; x < 320; x++ {
			img.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func testEngine(t *testing.T, start time.Time) (*Engine, *clock.Fake, *store.Store, string) {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })

	// Shrink interval to 15s for a faster day.
	cfg, _, err := st.GetSettings()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Capture.IntervalS = 15
	if _, err := st.SaveSettings(cfg); err != nil {
		t.Fatal(err)
	}
	st.SetSettingRaw("install_date", start.In(tz(t)).Format("2006-01-02"))

	clk := clock.NewFake(start)
	e := &Engine{
		Store: st, Log: slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})), Clk: clk,
		HB: &health.Heartbeat{}, DataRoot: dir,
		DiskFree: func(string) (uint64, error) { return 1 << 40, nil },
	}
	return e, clk, st, dir
}

func tz(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// End-to-end day: script rising color, advance the fake clock from noon
// past dusk, expect: one finished run, one GO event, day complete with
// best file + thumbs + sidecar, 12 stored frames.
func TestEndToEndDay(t *testing.T) {
	loc := tz(t)
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, loc)
	e, clk, st, _ := testEngine(t, start)

	cam := &store.Camera{
		ID: "CAMTEST01", Name: "sky", Type: "httpjpeg", Ref: "http://example/cam.jpg",
		Enabled: true, Role: "publish_primary", PublishEligible: true, State: "ok",
		ThresholdAbs: 12.0,
	}
	if err := st.InsertCamera(cam); err != nil {
		t.Fatal(err)
	}

	sc := newScriptedCam(t)
	// No AlertManager: e2e asserts the durable event latch itself.
	e.Fetchers = func(ctx context.Context) ([]CamFetcher, error) {
		return []CamFetcher{{Camera: *cam, Fetch: sc.fetch}}, nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { e.Run(ctx); close(done) }()

	// Advance one minute at a time until dusk + 40 min.
	now := clk.Now()
	ev, err := solar.For(loc, 40.6782, -73.9442, now)
	if err != nil {
		t.Fatal(err)
	}
	end := ev.Dusk.Add(40 * time.Minute)
	// Advance only while the engine is parked on its interval timer, so one
	// fake advance maps to one engine wake (an unpaced clock outruns the
	// engine's real fsync cost and fabricates broken-recency gaps).
	for step := 0; clk.Now().Before(end) && step < 3500; step++ {
		for i := 0; i < 5000 && clk.Pending() == 0; i++ {
			time.Sleep(time.Millisecond)
		}
		clk.Advance(15 * time.Second)
	}
	time.Sleep(30 * time.Millisecond) // let finalize land
	cancel()
	<-done

	date := start.Format("2006-01-02")

	// Run finished.
	run, ok, err := st.LatestFinishedRun(date)
	if err != nil || !ok {
		t.Fatalf("no run: ok=%v err=%v", ok, err)
	}
	if run.Status != "finished" {
		t.Fatalf("run status = %s", run.Status)
	}

	// Exactly one GO event, latched once.
	goEvent, ok, err := st.GetEventByKey(date + ":go")
	if err != nil || !ok {
		t.Fatalf("no go event: ok=%v err=%v", ok, err)
	}
	if goEvent.Kind != "go" || goEvent.Title == "" {
		t.Fatalf("bad event: %+v", goEvent)
	}

	// Day complete with best artifacts on disk.
	day, ok, err := st.GetDay(date)
	if err != nil || !ok {
		t.Fatalf("no day row: %v %v", ok, err)
	}
	if day.Status != "complete" {
		t.Fatalf("day status = %s, reason %s", day.Status, day.Reason)
	}
	if day.BestScore == nil || *day.BestScore < 25 {
		t.Fatalf("best score = %v, want orange frame > 25", day.BestScore)
	}
	for _, p := range []string{day.BestPath, day.Thumb480Path, day.Thumb240Path} {
		if p == "" {
			t.Fatalf("day paths incomplete: %+v", day)
		}
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("artifact missing: %s (%v)", p, err)
		}
	}
	if _, err := os.Stat(day.BestPath + ".json"); err != nil {
		t.Fatalf("sidecar missing: %v", err)
	}

	// Healthy sample stream stored (gray ramp + sustained oranges).
	frames, err := st.CameraDayFrames(cam.ID, date)
	if err != nil {
		t.Fatal(err)
	}
	if len(frames) < 12 {
		t.Fatalf("stored frames = %d, want >= 12", len(frames))
	}

	// /best never touched by retention: confirm one more tick doesn't remove it.
	if err := e.Retention(context.Background(), clk.Now().Format("2006-01-02")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(day.BestPath); err != nil {
		t.Fatalf("retention deleted best: %v", err)
	}
}

// Crash-point: a file on disk without a DB row is deleted; a row without a
// file is flagged; an interrupted run with a capturing day gets finalized.
func TestReconcileConverges(t *testing.T) {
	loc := tz(t)
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, loc)
	e, clk, st, dir := testEngine(t, start)

	cam := &store.Camera{
		ID: "CAMTEST01", Name: "sky", Type: "httpjpeg", Ref: "http://x/c.jpg",
		Enabled: true, Role: "publish_primary", PublishEligible: true, State: "ok",
	}
	st.InsertCamera(cam)

	date := start.Format("2006-01-02")
	yesterday := start.AddDate(0, 0, -1).Format("2006-01-02")

	// (a) orphan file + crash-leftover temp file.
	orphanDir := filepath.Join(dir, "frames", cam.ID, yesterday)
	os.MkdirAll(orphanDir, 0o755)
	os.WriteFile(filepath.Join(orphanDir, "235959.jpg"), []byte("orphan"), 0o644)
	os.WriteFile(filepath.Join(orphanDir, ".tmp-xyz"), []byte("partial"), 0o644)

	// (b) row without file.
	run := &store.Run{Mode: "production", LocalDate: yesterday, Status: "finished",
		PlannedStartUTC: start.AddDate(0, 0, -1).UnixMilli(), PlannedEndUTC: start.UnixMilli()}
	st.InsertRun(run)
	gone := filepath.Join(dir, "frames", cam.ID, yesterday, "200000-gone.jpg")
	score := 30.0
	medL := 50.0
	frac := 1.0
	chroma := 40.0
	st.InsertFrame(&store.FrameRow{
		RunID: run.ID, CameraID: cam.ID, LocalDate: yesterday,
		FetchedUTC: start.AddDate(0, 0, -1).UnixMilli(), Width: 320, Height: 240,
		SHA256: "aaaa", Score: &score, MedianL: &medL, SunsetPixelFraction: &frac,
		MeanChroma: &chroma, ScoringVersion: "s1", Valid: "ok", Path: gone,
	})

	// (c) interrupted run: status running, window passed, day capturing.
	irun := &store.Run{Mode: "production", LocalDate: date, Status: "running",
		PlannedStartUTC: start.Add(-2 * time.Hour).UnixMilli(), PlannedEndUTC: start.Add(-time.Hour).UnixMilli(),
		ActualStartUTC: start.Add(-2 * time.Hour).UnixMilli()}
	st.InsertRun(irun)
	st.UpsertDayStart(date)
	// Best candidate frame WITH file present.
	goodPath := filepath.Join(dir, "frames", cam.ID, date, "190000.jpg")
	os.MkdirAll(filepath.Dir(goodPath), 0o755)
	os.WriteFile(goodPath, encodeSolid(t, 255, 120, 40), 0o644)
	st.InsertFrame(&store.FrameRow{
		RunID: irun.ID, CameraID: cam.ID, LocalDate: date,
		FetchedUTC: start.Add(-90 * time.Minute).UnixMilli(), Width: 320, Height: 240,
		SHA256: "bbbb", Score: &score, MedianL: &medL, SunsetPixelFraction: &frac,
		MeanChroma: &chroma, ScoringVersion: "s1", Valid: "ok", Path: goodPath,
	})

	if err := e.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}

	// (a) orphans gone.
	if _, err := os.Stat(orphanDir); !os.IsNotExist(err) {
		t.Errorf("orphan dir survived: %v", err)
	}
	// (b) row flagged missing_file.
	valid, _, err := st.FrameValidByPath(gone)
	if err != nil || valid != "missing_file" {
		t.Errorf("missing row flag = %q err=%v, want missing_file", valid, err)
	}
	// (c) day finalized from stored frames.
	day, ok, _ := st.GetDay(date)
	if !ok || day.Status != "complete" {
		t.Fatalf("day after reconcile: %+v", day)
	}
	// Backfill: install_date is today, so [install, yesterday) is empty —
	// yesterday predates install and correctly gets NO row (nothing existed
	// to miss). Orphan sweep of its files is independent of day rows.
	day2, ok, _ := st.GetDay(yesterday)
	if ok {
		t.Fatalf("yesterday got a row predating install: %+v", day2)
	}
	_ = clk
}

func TestBackfillMissedDays(t *testing.T) {
	loc := tz(t)
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, loc)
	e, _, st, _ := testEngine(t, start)
	_ = e

	n, err := st.BackfillMissedDays("2026-08-16", "2026-08-19")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("backfilled %d, want 3", n)
	}
	for _, d := range []string{"2026-08-16", "2026-08-17", "2026-08-18"} {
		day, ok, _ := st.GetDay(d)
		if !ok || day.Status != "missed" {
			t.Fatalf("%s not missed: %+v", d, day)
		}
	}
	// Idempotent.
	if n, _ := st.BackfillMissedDays("2026-08-16", "2026-08-19"); n != 0 {
		t.Fatalf("second backfill inserted %d", n)
	}
}

var _ = config.Defaults
