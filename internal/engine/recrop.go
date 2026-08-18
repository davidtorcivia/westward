package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"os"
)

// RecropDay re-renders a completed day's published image from the kept
// original under /data/best/orig using the camera's CURRENT publish crop.
// Writes new hash-named files, updates the days row, deletes the old hashed
// files. The immutable-URL contract holds: content change ⇒ hash change ⇒
// new URL. Re-rendering with an unchanged crop is a no-op rewrite.
func (e *Engine) RecropDay(ctx context.Context, date string) error {
	if err := e.ensureSettings(); err != nil {
		return err
	}
	day, ok, err := e.Store.GetDay(date)
	if err != nil {
		return err
	}
	if !ok || day.Status != "complete" || day.BestPath == "" || day.BestCameraID == "" {
		return fmt.Errorf("day %s has no completed image to re-render", date)
	}

	scRaw, err := os.ReadFile(day.BestPath + ".json")
	if err != nil {
		return fmt.Errorf("sidecar: %w", err)
	}
	var sc BestSidecar
	if err := json.Unmarshal(scRaw, &sc); err != nil {
		return fmt.Errorf("sidecar corrupt: %w", err)
	}
	jpegBytes, err := os.ReadFile(sc.OrigPath)
	if err != nil {
		return fmt.Errorf("original %s: %w", sc.OrigPath, err)
	}

	cam, err := e.Store.GetCamera(day.BestCameraID)
	if err != nil {
		return fmt.Errorf("camera %s: %w", day.BestCameraID, err)
	}
	w, h := imageDims(jpegBytes)
	crop := cameraCrop(cam, w, h)

	pubBytes, err := cropOrFull(jpegBytes, crop)
	if err != nil {
		return err
	}
	hash := shortHash(shaHex(pubBytes))
	bestPath := fmt.Sprintf("%s/best/%s.%s.jpg", e.DataRoot, date, hash)
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
	if err := writeAtomic(bestPath, pubBytes); err != nil {
		return err
	}
	if err := writeAtomic(p480, t480); err != nil {
		return err
	}
	if err := writeAtomic(p240, t240); err != nil {
		return err
	}
	var cropArr *[4]float64
	if crop != nil {
		cropArr = &[4]float64{crop.X, crop.Y, crop.W, crop.H}
	}
	side, _ := json.Marshal(BestSidecar{
		Date: date, Score: sc.Score, Camera: cam.ID, CameraName: cam.Name,
		TakenUTC: sc.TakenUTC, Hash: hash, OrigHash: sc.OrigHash, OrigPath: sc.OrigPath,
		Crop: cropArr,
	})
	if err := writeAtomic(bestPath+".json", side); err != nil {
		return err
	}

	// Days row points at the new files before old ones are removed.
	if err := e.Store.CompleteDay(date, "complete", "", day.BestScore, cam.ID,
		day.BestTakenUTC, bestPath, p480, p240); err != nil {
		return err
	}
	// Delete only what actually changed (same crop ⇒ same hash ⇒ same names).
	for _, old := range []string{day.BestPath, day.Thumb480Path, day.Thumb240Path, day.BestPath + ".json"} {
		if old == "" || old == bestPath || old == p480 || old == p240 || old == bestPath+".json" {
			continue
		}
		if err := os.Remove(old); err != nil && !os.IsNotExist(err) {
			e.Log.Warn("recrop: old file left behind", "path", old, "err", err.Error())
		}
	}
	e.Log.Info("day re-rendered", "date", date, "hash", hash, "cropped", crop != nil)
	return nil
}

func imageDims(jpegBytes []byte) (int, int) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(jpegBytes))
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}
