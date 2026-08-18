package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/davidtorcivia/westward/internal/clock"
	"github.com/davidtorcivia/westward/internal/config"
	"github.com/davidtorcivia/westward/internal/forecast"
	"github.com/davidtorcivia/westward/internal/health"
	"github.com/davidtorcivia/westward/internal/score"
	"github.com/davidtorcivia/westward/internal/solar"
	"github.com/davidtorcivia/westward/internal/store"
)

// Engine owns the daily schedule. Timing flows through the injected clock;
// the loop recomputes the next event every wake, so settings changes apply
// to everything not already running.
type Engine struct {
	Store    *store.Store
	Log      *slog.Logger
	Clk      clock.Clock
	HB       *health.Heartbeat
	DataRoot string

	// Fetchers builds the capture set from current DB state each run.
	Fetchers func(ctx context.Context) ([]CamFetcher, error)
	// Alerts delivers durable events; nil = capture without delivery.
	Alerts *AlertManager
	// Forecast providers (nil = slots log only).
	OpenMeteo       *forecast.OpenMeteo
	SunsetHue       *forecast.SunsetHue
	SunsetHueKeyEnv string // env name for the sunsethue key
	// DiskFree returns free bytes of the data volume; overridable in tests.
	DiskFree func(path string) (uint64, error)

	settings config.Settings
	rev      int64
	loc      *time.Location
}

// CamFetcher pairs a source with its camera row.
type CamFetcher struct {
	Camera store.Camera
	Fetch  func(ctx context.Context) ([]byte, int, int, error) // jpeg, w, h
}

// AlertSender delivers a fired GO alert; phase 5 provides the real one.
type AlertSender interface {
	SendGO(ctx context.Context, localDate string, camName string, res score.Result, jpeg []byte, comps TriggerComponents) error
}

// ensureSettings self-heals direct calls (Finalize/Recrop/Retention from
// tests or admin) that skip Run's periodic reload.
func (e *Engine) ensureSettings() error {
	if e.loc == nil {
		return e.loadSettings()
	}
	return nil
}

func (e *Engine) loadSettings() error {
	s, rev, err := e.Store.GetSettings()
	if err != nil {
		return err
	}
	e.settings, e.rev = s, rev
	e.loc, err = time.LoadLocation(s.TZ)
	return err
}

// Run drives the engine until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) error {
	if e.DiskFree == nil {
		e.DiskFree = diskFree
	}
	if err := e.loadSettings(); err != nil {
		return err
	}
	if err := e.Reconcile(ctx); err != nil {
		return err
	}
	for {
		e.HB.Beat()
		if err := e.loadSettings(); err != nil {
			return err
		}
		now := e.Clk.Now()
		next, what := e.nextEvent(now)
		d := next.Sub(now)
		e.Log.Debug("engine waiting", "next", what, "at", next.In(e.loc).Format(time.RFC3339), "in", d.String())
		if d <= 0 {
			d = time.Second
		}
		select {
		case <-ctx.Done():
			return nil
		case <-e.Clk.After(d):
		}
		if err := e.tick(ctx); err != nil {
			e.Log.Error("engine tick failed", "err", err.Error())
		}
	}
}

// nextEvent returns the earliest of: daily plan (00:05), forecast, headsup,
// window start, window end, retention (03:30).
func (e *Engine) nextEvent(now time.Time) (time.Time, string) {
	today := now.In(e.loc)
	ev, err := solar.For(e.loc, e.settings.Lat, e.settings.Lon, today)
	if err != nil {
		// Solar failure (extreme latitudes): retry at next daily rollover.
		return e.atLocal(today, 0, 5), "daily-plan"
	}
	cands := []struct {
		t time.Time
		n string
	}{
		{e.atLocal(today, 0, 5), "daily-plan"},
		{ev.Sunset.Add(-40 * time.Minute), "forecast"},
		{ev.Sunset.Add(-35 * time.Minute), "headsup"},
		{ev.Sunset.Add(-time.Duration(e.settings.Capture.BeforeS) * time.Second), "window-start"},
		{ev.Dusk.Add(time.Duration(e.settings.Capture.AfterS) * time.Second), "window-end"},
		{e.atLocal(today, 3, 30), "retention"},
	}
	var best struct {
		t time.Time
		n string
	}
	for _, c := range cands {
		if c.t.After(now) && (best.n == "" || c.t.Before(best.t)) {
			best = c
		}
	}
	if best.n == "" {
		// All of today's events passed: next is tomorrow's daily plan.
		return e.atLocal(today, 0, 5).Add(24 * time.Hour), "daily-plan"
	}
	return best.t, best.n
}

func (e *Engine) atLocal(day time.Time, hour, min int) time.Time {
	d := day.In(e.loc)
	return time.Date(d.Year(), d.Month(), d.Day(), hour, min, 0, 0, e.loc)
}

// tick executes every event whose deadline has passed.
func (e *Engine) tick(ctx context.Context) error {
	now := e.Clk.Now().In(e.loc)
	ev, err := solar.For(e.loc, e.settings.Lat, e.settings.Lon, now)
	if err != nil {
		return err
	}
	date := now.Format("2006-01-02")

	// Nightly backup at 02:30, keep 7.
	if b := e.atLocal(now, 2, 30); !now.Before(b) {
		var lastB string
		if ok, _ := e.Store.GetSettingRaw("backup_last", &lastB); !ok || lastB != date {
			if err := e.Backup(e.DataRoot+"/backups", 7); err != nil {
				e.Log.Error("backup failed", "err", err.Error())
			} else {
				e.Store.SetSettingRaw("backup_last", date)
			}
		}
	}

	// Retention: due at/after 03:30 and not yet run today.
	if r := e.atLocal(now, 3, 30); !now.Before(r) {
		var last string
		ok, _ := e.Store.GetSettingRaw("retention_last", &last)
		if !ok || last != date {
			if err := e.Retention(ctx, date); err != nil {
				e.Log.Error("retention failed", "err", err.Error())
			} else {
				e.Store.SetSettingRaw("retention_last", date)
			}
		}
	}

	// Forecast slot at sunset-40m: fetch selected + comparison providers,
	// store observations (append-only).
	if t := ev.Sunset.Add(-40 * time.Minute); !now.Before(t) {
		var done string
		if ok, _ := e.Store.GetSettingRaw("forecast_last", &done); !ok || done != date {
			e.fetchForecasts(ctx, date, ev)
			e.Store.SetSettingRaw("forecast_last", date)
		}
	}

	// Heads-up slot at sunset-35m: quality floor gate over the selected
	// provider's latest observation; fallback to openmeteo on failure.
	if t := ev.Sunset.Add(-35 * time.Minute); !now.Before(t) {
		var done string
		if ok, _ := e.Store.GetSettingRaw("headsup_last", &done); !ok || done != date {
			e.Store.SetSettingRaw("headsup_last", date)
			if e.Alerts != nil {
				e.sendHeadsUp(ctx, date, ev)
			}
		}
	}

	// Capture window.
	wStart := ev.Sunset.Add(-time.Duration(e.settings.Capture.BeforeS) * time.Second)
	wEnd := ev.Dusk.Add(time.Duration(e.settings.Capture.AfterS) * time.Second)
	if !now.Before(wStart) && now.Before(wEnd) {
		day, _, err := e.Store.GetDay(date)
		if err != nil {
			return err
		}
		if day.Status != "capturing" {
			if day.Status == "complete" || day.Status == "failed" {
				return nil // window already finalized today
			}
			return e.RunWindow(ctx, date, ev, wStart, wEnd, "")
		}
		// capturing: a run row exists from a previous boot and the process
		// died mid-window; resume with a fresh run row.
		return e.RunWindow(ctx, date, ev, wStart, wEnd, "resumed")
	}
	if !now.Before(wEnd) {
		day, exists, err := e.Store.GetDay(date)
		if err != nil {
			return err
		}
		if exists && day.Status == "capturing" {
			if err := e.FinalizeDay(ctx, date); err != nil {
				e.Log.Error("finalize failed", "date", date, "err", err.Error())
			}
		}
	}
	return nil
}

// RunWindow executes one capture window: a run row, polling loop, trigger
// evaluation, then finalization. Blocks until window end or ctx cancel.
func (e *Engine) RunWindow(ctx context.Context, date string, ev solar.Events, wStart, wEnd time.Time, resumedFrom string) error {
	run := &store.Run{
		Mode: "production", LocalDate: date,
		PlannedStartUTC: wStart.UnixMilli(), PlannedEndUTC: wEnd.UnixMilli(),
		ActualStartUTC: e.Clk.Now().UnixMilli(), ConfigRevision: e.rev,
		ScoringVersion: score.ScoringVersion, Status: "running",
	}
	if resumedFrom != "" {
		run.ResumedFrom = resumedFrom
	}
	if err := e.Store.InsertRun(run); err != nil {
		return err
	}
	if err := e.Store.UpsertDayStart(date); err != nil {
		return err
	}
	e.Log.Info("capture window started", "date", date, "run", run.ID)

	fetchers, err := e.Fetchers(ctx)
	if err != nil {
		return err
	}
	interval := time.Duration(e.settings.Capture.IntervalS) * time.Second

	// Per-camera run state.
	type camState struct {
		f             CamFetcher
		trig          *TriggerState
		lastSHA       string
		params        TriggerParams
		roi           *score.ROI
		lastFramePath string
	}
	states := make([]camState, 0, len(fetchers))
	for _, f := range fetchers {
		ratio, deltaAbs, riseDelta, err := store.ParseTrigger(f.Camera.TriggerJSON, struct {
			Ratio     float64
			DeltaAbs  float64
			RiseDelta float64
		}{e.settings.Trigger.Ratio, e.settings.Trigger.DeltaAbs, e.settings.Trigger.RiseDelta})
		if err != nil {
			e.Log.Warn("bad trigger_json, using defaults", "camera", f.Camera.Name, "err", err.Error())
			ratio, deltaAbs, riseDelta = e.settings.Trigger.Ratio, e.settings.Trigger.DeltaAbs, e.settings.Trigger.RiseDelta
		}
		// Seed dedup from the camera's previous stored frame so a resumed
		// run doesn't re-archive the last pre-crash frame.
		prevSHA, _, _ := e.Store.PreviousFrameSHA(f.Camera.ID)
		states = append(states, camState{
			f: f, trig: NewTriggerState(interval), lastSHA: prevSHA,
			params: TriggerParams{
				ThresholdAbs: f.Camera.ThresholdAbs,
				Ratio:        ratio, DeltaAbs: deltaAbs, RiseDelta: riseDelta,
			},
			roi: cameraROI(f.Camera),
		})
	}

	lowDisk := false
	for {
		now := e.Clk.Now()
		if !now.Before(wEnd) || ctx.Err() != nil {
			break
		}
		e.HB.Beat()

		for i := range states {
			st := &states[i]
			if st.f.Camera.State == "stale" {
				continue
			}
			jpegBytes, w, h, err := st.f.Fetch(ctx)
			if err != nil {
				e.Log.Warn("fetch failed", "camera", st.f.Camera.Name, "err", err.Error())
				continue
			}
			// Duplicate detection: same SHA as camera's previous frame.
			sha := shaHex(jpegBytes)
			if sha == st.lastSHA {
				e.Store.BumpStaleStreak(st.f.Camera.ID, 1)
				continue // no history advance
			}
			st.lastSHA = sha
			e.Store.BumpStaleStreak(st.f.Camera.ID, -1)

			res, resErr := scoreFrame(jpegBytes, st.roi)
			if resErr != nil {
				e.Log.Warn("score failed", "camera", st.f.Camera.Name, "err", resErr.Error())
				continue
			}
			if !lowDisk {
				p := uniquePath(framePath(e.DataRoot, st.f.Camera.ID, date, e.Clk.Now().UnixMilli(), 0))
				st.lastFramePath = p
				if err := writeAtomic(p, jpegBytes); err != nil {
					e.Log.Error("frame write failed", "err", err.Error())
				} else {
					e.Store.InsertFrame(&store.FrameRow{
						RunID: run.ID, CameraID: st.f.Camera.ID, LocalDate: date,
						FetchedUTC: e.Clk.Now().UnixMilli(), Width: w, Height: h, SHA256: sha,
						Score: &res.Score, SunsetPixelFraction: &res.SunsetPixelFraction,
						MedianL: &res.MedianL, MeanChroma: &res.MeanQualifyingChroma,
						ScoringVersion: res.ScoringVersion, Valid: "ok", Path: p,
					})
				}
				if free, err := e.DiskFree(e.DataRoot); err == nil &&
					free < uint64(e.settings.DiskMinFreeMB)<<20 {
					lowDisk = true
					e.Log.Error("low disk: pausing frame writes", "free_mb", free>>20)
				}
			}

			st.trig.Append(e.Clk.Now(), res.Score)
			if st.trig.Evaluate(st.params) {
				imagePath := ""
				if !lowDisk {
					imagePath = st.lastFramePath
				}
				ok, err := e.Store.TryInsertEvent(&store.AlertEvent{
					EventKey:  date + ":go",
					LocalDate: date, Kind: "go",
					Title: fmt.Sprintf("GO — sunset is happening (%.1f)", res.Score),
					Body: fmt.Sprintf("Camera: %s. Peak usually 5–15 min after sundown (%s).",
						st.f.Camera.Name, ev.Sunset.In(e.loc).Format("15:04")),
					ImagePath:    imagePath,
					MetadataJSON: compsJSON(st.trig.Components),
				}, e.alertNotifierIDs())
				if err != nil {
					e.Log.Error("go latch failed", "err", err.Error())
				} else if ok {
					e.Log.Info("GO fired", "camera", st.f.Camera.Name, "score", res.Score)
				}
			}
		}

		// Sleep one interval or until window end.
		until := wEnd.Sub(e.Clk.Now())
		if until <= 0 {
			break
		}
		if interval < until {
			until = interval
		}
		select {
		case <-ctx.Done():
			e.Store.SetRunStatus(run.ID, "interrupted", e.Clk.Now().UnixMilli())
			return nil
		case <-e.Clk.After(until):
		}
	}

	if err := e.Store.SetRunStatus(run.ID, "finished", e.Clk.Now().UnixMilli()); err != nil {
		return err
	}
	return e.FinalizeDay(ctx, date)
}

func (e *Engine) alertNotifierIDs() []string {
	if e.Alerts == nil {
		return nil
	}
	return e.Alerts.notifierIDs()
}

// FinalizeDay applies archive eligibility, publication roles, writes best
// images + thumbs + sidecar, and completes the days row.
func (e *Engine) FinalizeDay(ctx context.Context, date string) error {
	if err := e.ensureSettings(); err != nil {
		return err
	}
	cams, err := e.Store.ListCameras()
	if err != nil {
		return err
	}
	ev, err := solar.For(e.loc, e.settings.Lat, e.settings.Lon, parseDate(date, e.loc))
	if err != nil {
		return err
	}
	cutoff := ev.Dusk.Add(time.Duration(e.settings.Archive.CutoffAfterDuskS) * time.Second)
	floor := e.settings.Archive.DarknessFloor

	type candidate struct {
		cam store.Camera
		f   store.FrameRow
	}
	best := map[string]candidate{} // per camera
	for _, cam := range cams {
		frames, err := e.Store.CameraDayFrames(cam.ID, date)
		if err != nil {
			return err
		}
		for _, f := range frames {
			if f.MedianL == nil || *f.MedianL < floor {
				continue
			}
			if time.UnixMilli(f.FetchedUTC).After(cutoff) {
				continue
			}
			if cur, ok := best[cam.ID]; !ok || (f.Score != nil && cur.f.Score != nil && *f.Score > *cur.f.Score) {
				best[cam.ID] = candidate{cam, f}
			}
		}
	}

	// Publication: primary first, then backups by publish_priority.
	pick := func(role string) (candidate, bool) {
		var out candidate
		found := false
		for _, c := range best {
			if c.cam.Role != role || !c.cam.PublishEligible {
				continue
			}
			if !found || c.cam.PublishPriority < out.cam.PublishPriority {
				out, found = c, true
			}
		}
		return out, found
	}
	var pub *candidate
	if c, ok := pick("publish_primary"); ok {
		pub = &c
	} else if c, ok := pick("publish_backup"); ok {
		pub = &c
	}

	if pub == nil {
		reason := "no archive-eligible frames"
		if len(best) > 0 {
			reason = "eligible frames exist but no publish-eligible camera"
		}
		e.Store.CompleteDay(date, "failed", reason, nil, "", 0, "", "", "")
		e.Log.Info("day finalized: failed", "date", date, "reason", reason)
		return nil
	}

	jpegBytes, err := readFile(pub.f.Path)
	if err != nil {
		e.Store.CompleteDay(date, "failed", "best frame file missing", nil, "", 0, "", "", "")
		return err
	}

	// Uncropped original, kept forever, never publicly served: recrop source.
	origHash := shortHash(pub.f.SHA256)
	origPath := fmt.Sprintf("%s/best/orig/%s.%s.jpg", e.DataRoot, date, origHash)
	if err := writeAtomic(origPath, jpegBytes); err != nil {
		return err
	}

	// Published image: publish_crop applied (nil = full frame), re-encoded
	// q90. The filename hash is of the PUBLISHED bytes, so URLs change with
	// content and immutable caching stays safe.
	crop := cameraCrop(pub.cam, pub.f.Width, pub.f.Height)
	if crop == nil && pub.cam.CropX != nil {
		e.Log.Warn("publish crop invalid for frame, publishing full frame",
			"camera", pub.cam.Name, "w", pub.f.Width, "h", pub.f.Height)
	}
	pubBytes, err := cropOrFull(jpegBytes, crop)
	if err != nil {
		return err
	}
	hash := shortHash(shaHex(pubBytes))
	bestPath := fmt.Sprintf("%s/best/%s.%s.jpg", e.DataRoot, date, hash)
	if err := writeAtomic(bestPath, pubBytes); err != nil {
		return err
	}
	t480, err := thumb(pubBytes, 480, 80)
	if err != nil {
		return err
	}
	t240, err := thumb(pubBytes, 240, 75)
	if err != nil {
		return err
	}
	p480 := fmt.Sprintf("%s/best/thumb/%s.%s.480.jpg", e.DataRoot, date, hash)
	p240 := fmt.Sprintf("%s/best/thumb/%s.%s.240.jpg", e.DataRoot, date, hash)
	if err := writeAtomic(p480, t480); err != nil {
		return err
	}
	if err := writeAtomic(p240, t240); err != nil {
		return err
	}
	// Sidecar enables days rebuild without the DB (plan §8.5.9) and recrop
	// from the original (change request: sidecar records crop + orig path).
	var cropArr *[4]float64
	if crop != nil {
		cropArr = &[4]float64{crop.X, crop.Y, crop.W, crop.H}
	}
	sidecar, _ := json.Marshal(BestSidecar{
		Date: date, Score: *pub.f.Score, Camera: pub.cam.ID, CameraName: pub.cam.Name,
		TakenUTC: pub.f.FetchedUTC, Hash: hash, OrigHash: origHash, OrigPath: origPath,
		Crop: cropArr,
	})
	if err := writeAtomic(bestPath+".json", sidecar); err != nil {
		return err
	}

	if err := e.Store.CompleteDay(date, "complete", "", pub.f.Score, pub.cam.ID,
		pub.f.FetchedUTC, bestPath, p480, p240); err != nil {
		return err
	}
	e.Log.Info("day finalized", "date", date, "score", *pub.f.Score, "camera", pub.cam.Name,
		"cropped", crop != nil)
	return nil
}

// Retention deletes frame dirs and rows past retention.frames_days. Never
// touches /data/best.
func (e *Engine) Retention(ctx context.Context, today string) error {
	if err := e.ensureSettings(); err != nil {
		return err
	}
	cutoff := parseDate(today, e.loc).AddDate(0, 0, -e.settings.RetentionFramesDays)
	cutoffStr := cutoff.Format("2006-01-02")
	if err := e.Store.DeleteFramesBefore(cutoffStr); err != nil {
		return err
	}
	if err := removeEmptyDirs(e.DataRoot + "/frames"); err != nil {
		e.Log.Warn("frame dir cleanup", "err", err.Error())
	}
	e.Log.Info("retention complete", "cutoff", cutoffStr)
	return nil
}

func diskFree(path string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, err
	}
	return st.Bavail * uint64(st.Bsize), nil
}

func parseDate(s string, loc *time.Location) time.Time {
	t, err := time.ParseInLocation("2006-01-02", s, loc)
	if err != nil {
		return time.Now().In(loc)
	}
	return t
}

// fetchForecasts stores observations for the selected provider (and the
// comparison provider when enabled and free/keys available).
func (e *Engine) fetchForecasts(ctx context.Context, date string, ev solar.Events) {
	selected := e.settings.Forecast.Provider
	fetch := func(p forecast.Provider, sel bool) {
		if p == nil {
			return
		}
		f, err := p.SunsetForecast(ctx, e.settings.Lat, e.settings.Lon, date)
		if err != nil {
			e.Log.Warn("forecast fetch failed", "provider", p.Name(), "err", err.Error())
			return
		}
		if err := e.Store.InsertForecastObservation(date, p.Name(), time.Now().UnixMilli(),
			f.EventUTC.UnixMilli(), f.Quality, f.Detail, string(f.RawJSON), f.AlgoVersion, sel); err != nil {
			e.Log.Error("forecast store failed", "provider", p.Name(), "err", err.Error())
		} else {
			e.Log.Info("forecast stored", "provider", p.Name(), "quality", f.Quality, "selected", sel)
		}
	}

	var sel, cmp forecast.Provider
	if e.OpenMeteo != nil {
		e.OpenMeteo.Tuning = forecast.Tuning{
			Peak:       e.settings.Forecast.OpenMeteo.Tuning.Peak,
			Width:      e.settings.Forecast.OpenMeteo.Tuning.Width,
			LowPenalty: e.settings.Forecast.OpenMeteo.Tuning.LowPenalty,
			HumPenalty: e.settings.Forecast.OpenMeteo.Tuning.HumPenalty,
			Overlap:    e.settings.Forecast.OpenMeteo.Tuning.Overlap,
		}
		cmp = e.OpenMeteo
	}
	if e.SunsetHue != nil {
		if key := os.Getenv(e.SunsetHueKeyEnv); key != "" {
			e.SunsetHue.APIKey = key
			if selected == "sunsethue" {
				sel = e.SunsetHue
			}
		}
	}
	if sel == nil && cmp != nil {
		sel = cmp // openmeteo default
	}
	if selected == "openmeteo" {
		sel, cmp = cmp, nil
		if e.settings.Forecast.ComparisonEnabled && e.SunsetHue != nil {
			if key := os.Getenv(e.SunsetHueKeyEnv); key != "" {
				cmp = e.SunsetHue
			}
		}
	}
	fetch(sel, true)
	fetch(cmp, false)
}

// sendHeadsUp gates the day's heads-up on the selected provider's quality.
func (e *Engine) sendHeadsUp(ctx context.Context, date string, ev solar.Events) {
	selected := e.settings.Forecast.Provider
	obs, ok, err := e.Store.LatestForecastObservation(date, selected)
	if err != nil {
		e.Log.Error("heads-up lookup failed", "err", err.Error())
		return
	}
	fallbackNote := ""
	if !ok {
		if selected == "sunsethue" {
			// provider failed: fall back to openmeteo observation if any
			obs, ok, _ = e.Store.LatestForecastObservation(date, "openmeteo")
			fallbackNote = " (sunsethue unavailable; openmeteo fallback)"
		}
		if !ok {
			e.Log.Warn("heads-up skipped: no forecast observation", "date", date)
			return
		}
	}
	if err := e.Alerts.HeadsUp(ctx, date, obs.Quality, e.settings.QualityFloor,
		ev.Sunset, obs.Detail+fallbackNote, obs.Provider); err != nil {
		e.Log.Error("heads-up failed", "err", err.Error())
	}
}

// Backup runs VACUUM INTO into dir and prunes to the newest keep files.
// /data/best (+ sidecars) and the backup file together form the durable set.
func (e *Engine) Backup(dir string, keep int) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	name := filepath.Join(dir, "westward-"+time.Now().UTC().Format("20060102T150405")+".db")
	if err := e.Store.Backup(name); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var backups []string
	for _, en := range entries {
		if strings.HasPrefix(en.Name(), "westward-") && strings.HasSuffix(en.Name(), ".db") {
			backups = append(backups, en.Name())
		}
	}
	sort.Strings(backups)
	for i := 0; i < len(backups)-keep; i++ {
		os.Remove(filepath.Join(dir, backups[i]))
	}
	e.Log.Info("backup written", "path", name, "keep", keep)
	return nil
}
