package engine

import (
	"encoding/json"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/davidtorcivia/westward/internal/store"
)

// finalizeFixture sets up one stored frame for a publish_primary camera and
// runs FinalizeDay. Returns day + parsed sidecar.
func finalizeFixture(t *testing.T, crop *[4]float64) (*Engine, store.Day, BestSidecar, string) {
	t.Helper()
	loc := tz(t)
	start := time.Date(2026, 8, 19, 12, 0, 0, 0, loc)
	e, _, st, dir := testEngine(t, start)

	cam := &store.Camera{
		ID: "CAMCROP001", Name: "sky", Type: "httpjpeg", Ref: "http://x/c.jpg",
		Enabled: true, Role: "publish_primary", PublishEligible: true, State: "ok",
	}
	if crop != nil {
		x, y, w, h := crop[0], crop[1], crop[2], crop[3]
		cam.CropX, cam.CropY, cam.CropW, cam.CropH = &x, &y, &w, &h
	}
	if err := st.InsertCamera(cam); err != nil {
		t.Fatal(err)
	}

	date := start.Format("2006-01-02")
	run := &store.Run{Mode: "production", LocalDate: date, Status: "finished"}
	if err := st.InsertRun(run); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertDayStart(date); err != nil {
		t.Fatal(err)
	}
	framePath := filepath.Join(dir, "frames", cam.ID, date, "190000.jpg")
	os.MkdirAll(filepath.Dir(framePath), 0o755)
	// 320x240 orange frame.
	jpegBytes := encodeSolid(t, 255, 120, 40)
	if err := os.WriteFile(framePath, jpegBytes, 0o644); err != nil {
		t.Fatal(err)
	}
	scoreVal, medL, frac, chroma := 88.0, 60.0, 1.0, 40.0
	if err := st.InsertFrame(&store.FrameRow{
		RunID: run.ID, CameraID: cam.ID, LocalDate: date,
		FetchedUTC: start.Add(-90 * time.Minute).UnixMilli(), Width: 320, Height: 240,
		SHA256: shaHex(jpegBytes), Score: &scoreVal, MedianL: &medL,
		SunsetPixelFraction: &frac, MeanChroma: &chroma,
		ScoringVersion: "s1", Valid: "ok", Path: framePath,
	}); err != nil {
		t.Fatal(err)
	}

	if err := e.FinalizeDay(t.Context(), date); err != nil {
		t.Fatal(err)
	}
	day, ok, err := st.GetDay(date)
	if err != nil || !ok || day.Status != "complete" {
		t.Fatalf("day: ok=%v err=%v %+v", ok, err, day)
	}
	scRaw, err := os.ReadFile(day.BestPath + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var sc BestSidecar
	if err := json.Unmarshal(scRaw, &sc); err != nil {
		t.Fatal(err)
	}
	return e, day, sc, date
}

func dims(t *testing.T, path string) (int, int) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	cfg, err := jpeg.DecodeConfig(f)
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Width, cfg.Height
}

// Crop set: published image is cropped (aspect preserved by crop), original
// kept uncropped at /best/orig, sidecar records crop + orig path.
func TestFinalizeWithCrop(t *testing.T) {
	_, day, sc, _ := finalizeFixture(t, &[4]float64{0.1, 0.1, 0.8, 0.6})

	// 320x240 × (0.8, 0.6) = 256x144 published.
	w, h := dims(t, day.BestPath)
	if w != 256 || h != 144 {
		t.Fatalf("published dims %dx%d, want 256x144", w, h)
	}
	// Original uncropped at orig path.
	ow, oh := dims(t, sc.OrigPath)
	if ow != 320 || oh != 240 {
		t.Fatalf("orig dims %dx%d, want 320x240", ow, oh)
	}
	if sc.OrigPath == day.BestPath || sc.OrigHash == sc.Hash {
		t.Fatal("orig and published must be distinct")
	}
	if sc.Crop == nil || (*sc.Crop)[0] != 0.1 || (*sc.Crop)[2] != 0.8 {
		t.Fatalf("sidecar crop: %+v", sc.Crop)
	}
	if day.Thumb480Path == "" || day.Thumb240Path == "" {
		t.Fatal("thumbs missing")
	}
	// Thumbs never upscale: the 256-wide crop is its own 480 thumb; the
	// 240 thumb scales down.
	tw, th := dims(t, day.Thumb480Path)
	if tw != 256 || th != 144 {
		t.Fatalf("thumb480 dims %dx%d, want 256x144 (no upscale)", tw, th)
	}
	sw, sh := dims(t, day.Thumb240Path)
	if sw != 240 || sh != 135 {
		t.Fatalf("thumb240 dims %dx%d, want 240x135", sw, sh)
	}
}

// No crop: published image is the full frame (re-encoded), orig still kept.
func TestFinalizeWithoutCrop(t *testing.T) {
	_, day, sc, _ := finalizeFixture(t, nil)

	w, h := dims(t, day.BestPath)
	if w != 320 || h != 240 {
		t.Fatalf("published dims %dx%d, want full 320x240", w, h)
	}
	if sc.Crop != nil {
		t.Fatalf("sidecar crop should be nil: %+v", sc.Crop)
	}
	ow, _ := dims(t, sc.OrigPath)
	if ow != 320 {
		t.Fatal("orig not kept for uncropped publish")
	}
}

// Re-render: change the camera crop, recrop from orig; days row moves to
// the new hash file, old hashed files are deleted, orig untouched.
func TestRecropDay(t *testing.T) {
	e, day, sc, date := finalizeFixture(t, nil)
	st := e.Store

	// Change crop on the camera, then re-render.
	cam, err := st.GetCamera(day.BestCameraID)
	if err != nil {
		t.Fatal(err)
	}
	x, y, w, h := 0.0, 0.0, 0.5, 1.0
	cam.CropX, cam.CropY, cam.CropW, cam.CropH = &x, &y, &w, &h
	if err := st.UpdateCamera(&cam); err != nil {
		t.Fatal(err)
	}

	oldBest, old480, old240 := day.BestPath, day.Thumb480Path, day.Thumb240Path
	if err := e.RecropDay(t.Context(), date); err != nil {
		t.Fatal(err)
	}

	day2, _, err := st.GetDay(date)
	if err != nil {
		t.Fatal(err)
	}
	if day2.BestPath == oldBest {
		t.Fatal("days row not updated to new hash path")
	}
	// 320x240 × (0.5, 1.0) = 160x240.
	w2, h2 := dims(t, day2.BestPath)
	if w2 != 160 || h2 != 240 {
		t.Fatalf("recrop dims %dx%d, want 160x240", w2, h2)
	}
	// Old hashed files gone; orig still present.
	for _, old := range []string{oldBest, old480, old240, oldBest + ".json"} {
		if _, err := os.Stat(old); !os.IsNotExist(err) {
			t.Errorf("old file survived recrop: %s", old)
		}
	}
	if _, err := os.Stat(sc.OrigPath); err != nil {
		t.Fatalf("original deleted by recrop: %v", err)
	}
	// Sidecar of the new file points at the same orig and new crop.
	var sc2 BestSidecar
	raw, _ := os.ReadFile(day2.BestPath + ".json")
	if err := json.Unmarshal(raw, &sc2); err != nil {
		t.Fatal(err)
	}
	if sc2.OrigPath != sc.OrigPath || sc2.Crop == nil {
		t.Fatalf("new sidecar: %+v", sc2)
	}

	// Re-render again with an UNCHANGED crop: same hash, same paths, no churn.
	if err := e.RecropDay(t.Context(), date); err != nil {
		t.Fatal(err)
	}
	day3, _, _ := st.GetDay(date)
	if day3.BestPath != day2.BestPath {
		t.Fatalf("unchanged recrop changed the URL: %s -> %s", day2.BestPath, day3.BestPath)
	}
}

// Retention never touches /best/orig.
func TestRetentionSparesOrig(t *testing.T) {
	e, _, sc, today := finalizeFixture(t, nil)
	if err := e.Retention(t.Context(), today); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sc.OrigPath); err != nil {
		t.Fatalf("retention deleted /best/orig: %v", err)
	}
}
