package score

import (
	"errors"
	"image"
	"image/color"
	"math"
	"testing"
)

// Colorimetry goldens, spec §7. Tolerance ±0.5.
func TestToLabGoldens(t *testing.T) {
	cases := []struct {
		name                string
		r, g, b             uint8
		wantL, wantA, wantB float64
		wantHue             float64 // NaN = don't check
	}{
		{"red", 255, 0, 0, 53.24, 80.09, 67.20, math.NaN()},
		{"orange", 255, 165, 0, 74.94, 23.93, 78.95, 73.1},
		{"gray", 128, 128, 128, 53.59, 0, 0, math.NaN()},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			L, a, b := toLab(c.r, c.g, c.b)
			check := func(name string, got, want float64) {
				if diff := math.Abs(got - want); diff > 0.5 {
					t.Errorf("%s = %.2f, want %.2f (±0.5)", name, got, want)
				}
			}
			check("L*", L, c.wantL)
			check("a*", a, c.wantA)
			check("b*", b, c.wantB)
			if !math.IsNaN(c.wantHue) {
				hue := math.Atan2(b, a) * 180 / math.Pi
				if diff := math.Abs(hue - c.wantHue); diff > 0.5 {
					t.Errorf("hue = %.2f, want %.2f (±0.5)", hue, c.wantHue)
				}
			}
		})
	}
}

// sRGB gray must have ~zero chroma.
func TestGrayChroma(t *testing.T) {
	_, a, b := toLab(128, 128, 128)
	if c := math.Sqrt(a*a + b*b); c > 0.5 {
		t.Errorf("gray C* = %.2f, want ≈0", c)
	}
}

func TestPixelRectMatrix(t *testing.T) {
	b := image.Rect(0, 0, 1920, 1080)
	cases := []struct {
		name    string
		roi     ROI
		wantErr bool
	}{
		{"nan", ROI{X: math.NaN(), Y: 0, W: 1, H: 0.45}, true},
		{"negative", ROI{X: -0.1, Y: 0, W: 1, H: 0.45}, true},
		{"above1", ROI{X: 0, Y: 0, W: 1.2, H: 0.45}, true},
		{"zero area", ROI{X: 0.5, Y: 0.5, W: 0, H: 0}, true},
		{"tiny", ROI{X: 0.99, Y: 0.99, W: 0.005, H: 0.005}, true},     // maps under 64px
		{"clamp overflow", ROI{X: 0.8, Y: 0, W: 0.5, H: 0.45}, false}, // x+w clamps to 0.2
		{"default-ish", ROI{X: 0, Y: 0, W: 1, H: 0.45}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := PixelRect(c.roi, b)
			if c.wantErr && err == nil {
				t.Fatal("expected error")
			}
			if !c.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}

	// Exact mapping: floor origin, round size.
	r, err := PixelRect(ROI{X: 0.25, Y: 0.5, W: 0.5, H: 0.25}, b)
	if err != nil {
		t.Fatal(err)
	}
	if r.Min.X != 480 || r.Min.Y != 540 || r.Dx() != 960 || r.Dy() != 270 {
		t.Errorf("rect = %v, want origin (480,540) size 960x270", r)
	}

	// Dimension change: same ROI on a different frame recomputes the rect.
	b2 := image.Rect(0, 0, 1280, 720)
	r2, err := PixelRect(ROI{X: 0.25, Y: 0.5, W: 0.5, H: 0.25}, b2)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Min.X != 320 || r2.Min.Y != 360 || r2.Dx() != 640 || r2.Dy() != 180 {
		t.Errorf("rect on 1280x720 = %v", r2)
	}

	// Minimum area boundary: exactly 64x64 passes.
	_, err = PixelRect(ROI{X: 0, Y: 0, W: 64.0 / 3000, H: 64.0 / 3000}, image.Rect(0, 0, 3000, 3000))
	if err != nil {
		t.Errorf("exactly-64px roi rejected: %v", err)
	}
}

func TestScoreInvalidROINilResult(t *testing.T) {
	img := solidImage(200, 200, 255, 0, 0)
	bad := ROI{X: math.NaN()}
	res, err := Score(img, &bad)
	if err == nil {
		t.Fatal("expected error")
	}
	if res.Score != 0 {
		t.Fatalf("score on error = %v", res.Score)
	}
	var e *ErrInvalidROI
	if !errors.As(err, &e) {
		t.Fatalf("error type = %T", err)
	}
}

func TestMedian(t *testing.T) {
	cases := []struct {
		in   []float64
		want float64
	}{
		{[]float64{5}, 5},
		{[]float64{1, 3, 2}, 2},
		{[]float64{4, 1, 3, 2}, 2.5},
		{[]float64{9, 1}, 5},
	}
	for _, c := range cases {
		if got := median(c.in); got != c.want {
			t.Errorf("median(%v) = %v, want %v", c.in, got, c.want)
		}
	}
	// Caller's slice must not be reordered.
	v := []float64{3, 1, 2}
	median(v)
	if v[0] != 3 || v[1] != 1 || v[2] != 2 {
		t.Errorf("median mutated input: %v", v)
	}
}

// solidImage builds a uniform-color image; fills the whole rect.
func solidImage(w, h int, r8, g8, b8 uint8) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, color.RGBA{R: r8, G: g8, B: b8, A: 255})
		}
	}
	return img
}
