package engine

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/image/draw"

	"github.com/davidtorcivia/westward/internal/score"
	"github.com/davidtorcivia/westward/internal/store"
)

// writeAtomic implements the crash-safe persistence order: temp file in the
// same directory, fsync, rename (plan §8.2).
func writeAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name) // no-op after successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// framePath builds /data/frames/{camera}/{date}/{HHMMSS}.jpg with a
// uniqueness suffix when two frames land in the same second.
func framePath(root, cameraID, localDate string, ms int64, dup int) string {
	sec := ms / 1000
	name := fmt.Sprintf("%02d%02d%02d", (sec/3600)%24, (sec/60)%60, sec%60)
	if dup > 0 {
		name = fmt.Sprintf("%s-%d", name, dup+1)
	}
	return filepath.Join(root, "frames", cameraID, localDate, name+".jpg")
}

func uniquePath(p string) string {
	if _, err := os.Stat(p); os.IsNotExist(err) {
		return p
	}
	for i := 1; ; i++ {
		c := strings.TrimSuffix(p, ".jpg")
		np := fmt.Sprintf("%s-%d.jpg", c, i)
		if _, err := os.Stat(np); os.IsNotExist(err) {
			return np
		}
	}
}

// shortHash returns the first 8 hex chars of a sha256 hex string.
func shortHash(hex string) string {
	if len(hex) > 8 {
		return hex[:8]
	}
	return hex
}

// thumb generates a width-w JPEG (aspect preserved) from jpegBytes.
func thumb(jpegBytes []byte, w int, quality int) ([]byte, error) {
	img, err := jpeg.Decode(bytes.NewReader(jpegBytes))
	if err != nil {
		return nil, err
	}
	b := img.Bounds()
	if b.Dx() <= w {
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: quality}); err != nil {
			return nil, err
		}
		return buf.Bytes(), nil
	}
	h := max(1, b.Dy()*w/b.Dx())
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.BiLinear.Scale(dst, dst.Bounds(), img, b, draw.Src, nil)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: quality}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// cameraCrop validates a stored publish crop against real image bounds
// (min 64x64 mapped; NaN/range junk falls back to full frame, never fails
// the day). Returns nil for "no crop" (publish full frame).
func cameraCrop(cam store.Camera, imgW, imgH int) *score.ROI {
	if cam.CropX == nil || cam.CropY == nil || cam.CropW == nil || cam.CropH == nil {
		return nil
	}
	roi := score.ROI{X: *cam.CropX, Y: *cam.CropY, W: *cam.CropW, H: *cam.CropH}
	if _, err := score.PixelRect(roi, image.Rect(0, 0, imgW, imgH)); err != nil {
		return nil // invalid crop: fail open to full frame
	}
	return &roi
}

// cropOrFull returns the published bytes: crop applied (or the full frame
// when roi is nil), re-encoded q90. Deterministic pipeline for both paths
// so the content hash always reflects the published bytes.
func cropOrFull(jpegBytes []byte, roi *score.ROI) ([]byte, error) {
	img, _, err := image.Decode(bytes.NewReader(jpegBytes))
	if err != nil {
		return nil, err
	}
	src := image.Image(img)
	if roi != nil {
		rect, err := score.PixelRect(*roi, img.Bounds())
		if err != nil {
			return nil, err
		}
		if cr, ok := img.(interface {
			SubImage(r image.Rectangle) image.Image
		}); ok {
			src = cr.SubImage(rect)
		} else {
			cp := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
			draw.Draw(cp, cp.Bounds(), img, rect.Min, draw.Src)
			src = cp
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, src, &jpeg.Options{Quality: 90}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// BestSidecar is the recovery record written next to every published best
// image and used by RecropDay and days-row rebuilds.
type BestSidecar struct {
	Date       string      `json:"date"`
	Score      float64     `json:"score"`
	Camera     string      `json:"camera"`
	CameraName string      `json:"camera_name"`
	TakenUTC   int64       `json:"taken_utc"`
	Hash       string      `json:"hash"`
	OrigHash   string      `json:"orig_hash"`
	OrigPath   string      `json:"orig_path"`
	Crop       *[4]float64 `json:"crop,omitempty"` // x,y,w,h normalized; nil = full frame
}
