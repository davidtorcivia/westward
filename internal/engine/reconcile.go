package engine

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"

	"github.com/davidtorcivia/westward/internal/score"
	"github.com/davidtorcivia/westward/internal/store"
)

func shaHex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// scoreFrame decodes and scores with the camera's ROI (nil ROI = default).
func scoreFrame(jpegBytes []byte, roi *score.ROI) (score.Result, error) {
	img, err := jpeg.Decode(bytes.NewReader(jpegBytes))
	if err != nil {
		return score.Result{}, err
	}
	return score.Score(img, roi)
}

func compsJSON(c TriggerComponents) string {
	b, _ := json.Marshal(map[string]float64{
		"recent": c.Recent, "baseline": c.Baseline, "prior": c.Prior,
	})
	return string(b)
}

func readFile(p string) ([]byte, error) {
	return os.ReadFile(p)
}

// CameraROIFromStore converts stored nullable ROI columns into a score ROI
// (exported twin for admin preview scoring).
func CameraROIFromStore(cam store.Camera) *score.ROI {
	return cameraROI(cam)
}

// cameraROI converts stored nullable ROI columns into a score ROI.
func cameraROI(cam store.Camera) *score.ROI {
	if cam.ROIX == nil || cam.ROIY == nil || cam.ROIW == nil || cam.ROIH == nil {
		return nil // default: top 45%
	}
	return &score.ROI{X: *cam.ROIX, Y: *cam.ROIY, W: *cam.ROIW, H: *cam.ROIH}
}

func removeEmptyDirs(root string) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // best effort
		}
		if !d.IsDir() || path == root {
			return nil
		}
		entries, err := os.ReadDir(path)
		if err == nil && len(entries) == 0 {
			os.Remove(path)
		}
		return nil
	})
}

// Reconcile runs the §8.5 startup sequence: backfill missed days, finalize
// interrupted windows, catch up retention, sweep orphans. Safe to call
// before Run: it (re)loads settings itself.
func (e *Engine) Reconcile(ctx context.Context) error {
	if err := e.loadSettings(); err != nil {
		return err
	}
	now := e.Clk.Now().In(e.loc)
	today := now.Format("2006-01-02")

	// 2. Backfill missed days from install_date.
	var install string
	ok, err := e.Store.GetSettingRaw("install_date", &install)
	if err != nil {
		return err
	}
	if ok && install < today {
		if n, err := e.Store.BackfillMissedDays(install, today); err != nil {
			return err
		} else if n > 0 {
			e.Log.Info("backfilled missed days", "count", n)
		}
	}

	// 3. Finalize any production run whose window ended but day not complete.
	if n, err := e.Store.MarkInterruptedRuns(e.Clk.Now().UnixMilli()); err != nil {
		return err
	} else if n > 0 {
		e.Log.Info("marked interrupted runs", "count", n)
	}
	// Days stuck in capturing whose window has passed get finalized now.
	// MarkInterruptedRuns already flipped every run whose planned end passed,
	// so a capturing day with no 'running' run is a crashed-and-ended window
	// (or a past day): finalize. A mid-window crash keeps its 'running' run
	// and resumes via the tick loop instead.
	days, err := e.Store.DaysWithStatus("capturing")
	if err != nil {
		return err
	}
	for _, d := range days {
		running, err := e.Store.HasRunningRun(d)
		if err != nil {
			return err
		}
		if running {
			continue
		}
		if err := e.FinalizeDay(ctx, d); err != nil {
			e.Log.Error("reconcile finalize failed", "date", d, "err", err.Error())
		}
	}

	// 7. Retention catch-up.
	var last string
	if ok, _ := e.Store.GetSettingRaw("retention_last", &last); !ok || last != today {
		if err := e.Retention(ctx, today); err != nil {
			e.Log.Error("reconcile retention failed", "err", err.Error())
		} else {
			e.Store.SetSettingRaw("retention_last", today)
		}
	}

	// 9. Orphan sweep: files without rows are deleted (re-capturable data,
	// never user-facing); rows without files are marked.
	nOrph, err := e.sweepOrphanFrames()
	if err != nil {
		e.Log.Error("orphan sweep failed", "err", err.Error())
	} else if nOrph > 0 {
		e.Log.Info("orphan files removed", "count", nOrph)
	}
	return nil
}

// sweepOrphanFrames removes frame files lacking DB rows and marks DB rows
// whose files are gone.
func (e *Engine) sweepOrphanFrames() (int, error) {
	known := map[string]bool{}
	if err := e.Store.ForEachFramePath(func(path string) { known[path] = true }); err != nil {
		return 0, err
	}
	removed := 0
	root := filepath.Join(e.DataRoot, "frames")
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".tmp-") {
			os.Remove(path) // crash leftovers
			removed++
			return nil
		}
		if !known[path] {
			os.Remove(path)
			removed++
		}
		return nil
	})
	if err != nil {
		return removed, err
	}
	// Rows without files: collect first (single connection: no writes while
	// a Rows iterator is open), then stat each and flag individually.
	var pairs [][2]any
	if err := e.Store.ForEachFrame(func(id int64, path string) {
		pairs = append(pairs, [2]any{id, path})
	}); err != nil {
		return removed, err
	}
	var missing int
	for _, p := range pairs {
		if _, err := os.Stat(p[1].(string)); os.IsNotExist(err) {
			e.Store.MarkFrameMissing(p[0].(int64))
			missing++
		}
	}
	if missing > 0 {
		e.Log.Warn("frame rows missing files", "count", missing)
	}
	// Empty dirs left by the deletions above (retention's dir cleanup runs
	// earlier in the reconcile sequence).
	if err := removeEmptyDirs(root); err != nil {
		e.Log.Warn("orphan dir cleanup", "err", err.Error())
	}
	return removed, nil
}
