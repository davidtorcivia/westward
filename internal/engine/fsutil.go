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
