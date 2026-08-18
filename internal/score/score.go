// Package score implements the deterministic sunset-color scorer (scoring
// version s1).
//
// Honesty statement: frames are decoded as nominal 8-bit sRGB; embedded ICC
// profiles are not color-managed. Scores are consistent within one camera +
// ROI + scoring version; they are not absolute measurements across cameras.
// Bump ScoringVersion on any change to this file's math.
package score

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"math"

	"golang.org/x/image/draw"
)

const ScoringVersion = "s1"

// ROI is a normalized rectangle in [0,1]. Nil means top 45% of the frame.
type ROI struct {
	X, Y, W, H float64
}

// DefaultROI is the top 45% of the frame.
var DefaultROI = ROI{X: 0, Y: 0, W: 1, H: 0.45}

// Result carries the score plus the diagnostics required for later tuning.
type Result struct {
	ScoringVersion       string
	Score                float64 // 0..100
	SunsetPixelFraction  float64
	MedianL              float64
	MeanQualifyingChroma float64
}

// Pixel classification constants (spec §7).
const (
	hueMin     = -45.0 // degrees
	hueMax     = 100.0
	lMin       = 12.0
	lMax       = 97.0
	chromaMin  = 15.0
	chromaSpan = 45.0 // weight reaches 1 at C* = 60
)

// ErrInvalidROI marks frames whose ROI cannot map to a usable pixel rect.
type ErrInvalidROI struct{ Msg string }

func (e *ErrInvalidROI) Error() string { return "invalid roi: " + e.Msg }

// PixelRect maps a normalized ROI onto image bounds. NaN, negative, or
// out-of-range values are rejected; x+w/y+h are clamped to ≤1.
func PixelRect(roi ROI, b image.Rectangle) (image.Rectangle, error) {
	for _, v := range []float64{roi.X, roi.Y, roi.W, roi.H} {
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return image.Rectangle{}, &ErrInvalidROI{"nan or inf component"}
		}
	}
	if roi.X < 0 || roi.Y < 0 || roi.W < 0 || roi.H < 0 {
		return image.Rectangle{}, &ErrInvalidROI{"negative component"}
	}
	if roi.X > 1 || roi.Y > 1 || roi.W > 1 || roi.H > 1 {
		return image.Rectangle{}, &ErrInvalidROI{"component above 1"}
	}
	// Clamp x+w ≤ 1, y+h ≤ 1.
	if roi.X+roi.W > 1 {
		roi.W = 1 - roi.X
	}
	if roi.Y+roi.H > 1 {
		roi.H = 1 - roi.Y
	}
	x := b.Min.X + int(math.Floor(roi.X*float64(b.Dx())))
	y := b.Min.Y + int(math.Floor(roi.Y*float64(b.Dy())))
	w := int(math.Round(roi.W * float64(b.Dx())))
	h := int(math.Round(roi.H * float64(b.Dy())))
	r := image.Rect(x, y, x+w, y+h)
	if r.Dx() < 64 || r.Dy() < 64 {
		return image.Rectangle{}, &ErrInvalidROI{
			fmt.Sprintf("roi maps to %dx%d px, minimum is 64x64", r.Dx(), r.Dy())}
	}
	return r, nil
}

// downscale returns the ROI sub-image, resized so the longer edge ≤ 320 px
// using BiLinear exactly (determinism requirement).
func downscale(sub image.Image) image.Image {
	b := sub.Bounds()
	longer := max(b.Dx(), b.Dy())
	if longer <= 320 {
		return sub
	}
	scale := 320.0 / float64(longer)
	w := max(1, int(math.Round(float64(b.Dx())*scale)))
	h := max(1, int(math.Round(float64(b.Dy())*scale)))
	dst := image.NewRGBA(image.Rect(0, 0, w, h))
	draw.BiLinear.Scale(dst, dst.Bounds(), sub, b, draw.Src, nil)
	return dst
}

// sRGB 8-bit → CIELAB (D65). Inputs 0..255.
func toLab(r8, g8, b8 uint8) (L, a, b float64) {
	rl := srgbToLinear(float64(r8) / 255.0)
	gl := srgbToLinear(float64(g8) / 255.0)
	bl := srgbToLinear(float64(b8) / 255.0)
	// sRGB → XYZ, D65 matrix.
	X := 0.4124564*rl + 0.3575761*gl + 0.1804375*bl
	Y := 0.2126729*rl + 0.7151522*gl + 0.0721750*bl
	Z := 0.0193339*rl + 0.1191920*gl + 0.9503041*bl
	// Normalize by D65 white then apply CIELAB forward transform.
	fx := labF(X / 0.95047)
	fy := labF(Y / 1.0)
	fz := labF(Z / 1.08883)
	L = 116*fy - 16
	a = 500 * (fx - fy)
	b = 200 * (fy - fz)
	return
}

func srgbToLinear(c float64) float64 {
	if c <= 0.04045 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// labF is the CIELAB f(t) with the 6/29 cusp.
func labF(t float64) float64 {
	const eps = 216.0 / 24389.0 // (6/29)^3
	const kappaDiv3 = 1 / (3 * (29.0 / 6.0) * (29.0 / 6.0))
	if t > eps {
		return math.Cbrt(t)
	}
	return kappaDiv3*t + 4.0/29.0
}

// Score scores one frame. img must already be decoded; roi nil = default.
func Score(img image.Image, roi *ROI) (Result, error) {
	if roi == nil {
		r := DefaultROI
		roi = &r
	}
	rect, err := PixelRect(*roi, img.Bounds())
	if err != nil {
		return Result{ScoringVersion: ScoringVersion}, err
	}

	type rgbaimage = image.Image
	var sub rgbaimage = img
	if rect != img.Bounds() {
		if cr, ok := img.(interface {
			SubImage(r image.Rectangle) image.Image
		}); ok {
			sub = cr.SubImage(rect)
		} else {
			// Copy the rect (rare; most decoders support SubImage).
			cp := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
			draw.Draw(cp, cp.Bounds(), img, rect.Min, draw.Src)
			sub = cp
		}
	}
	small := downscale(sub)
	b := small.Bounds()

	n := 0
	var weightSum float64
	var lums []float64
	var chromaSum float64
	var chromaN int
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			pr, pg, pb, _ := small.At(x, y).RGBA()
			L, a, bb := toLab(uint8(pr>>8), uint8(pg>>8), uint8(pb>>8))
			n++
			lums = append(lums, L)
			c := math.Sqrt(a*a + bb*bb)
			h := math.Atan2(bb, a) * 180 / math.Pi
			if h >= hueMin && h <= hueMax && L >= lMin && L <= lMax && c >= chromaMin {
				weightSum += math.Min(1, (c-chromaMin)/chromaSpan)
				chromaSum += c
				chromaN++
			}
		}
	}
	if n == 0 {
		return Result{ScoringVersion: ScoringVersion}, &ErrInvalidROI{"empty roi"}
	}
	res := Result{
		ScoringVersion:      ScoringVersion,
		Score:               math.Round(10000*weightSum/float64(n)) / 100,
		SunsetPixelFraction: float64(chromaN) / float64(n),
		MedianL:             median(lums),
	}
	if chromaN > 0 {
		res.MeanQualifyingChroma = chromaSum / float64(chromaN)
	}
	return res, nil
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	// Copy to avoid mutating caller's slice.
	s := append([]float64(nil), v...)
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
	m := len(s) / 2
	if len(s)%2 == 1 {
		return s[m]
	}
	return (s[m-1] + s[m]) / 2
}

// ScoreBytes decodes a JPEG and scores it with the given ROI (nil = default).
// w/h come from the caller (already sniffed by the fetch path).
func ScoreBytes(b []byte, roi *ROI, _, _ int) (Result, error) {
	img, err := jpeg.Decode(bytes.NewReader(b))
	if err != nil {
		return Result{ScoringVersion: ScoringVersion}, err
	}
	return Score(img, roi)
}
