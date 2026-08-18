//go:build ignore

// Generates the synthetic sky fixtures under testdata/frames/.
// Run: go run testdata/frames/gen/main.go
package main

import (
	"image"
	"image/color"
	"image/jpeg"
	"math/rand"
	"os"
	"path/filepath"
)

func main() {
	dir, _ := os.Getwd()
	out := filepath.Join(dir, "testdata", "frames")
	os.MkdirAll(out, 0o755)

	write := func(name string, img image.Image) {
		f, err := os.Create(filepath.Join(out, name))
		if err != nil {
			panic(err)
		}
		defer f.Close()
		if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 92}); err != nil {
			panic(err)
		}
	}

	// 640x480, all fixtures.
	const W, H = 640, 480

	// gray_day: overcast, flat gray with slight noise.
	gray := image.NewRGBA(image.Rect(0, 0, W, H))
	rnd := rand.New(rand.NewSource(1))
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			n := uint8(rnd.Intn(9) - 4)
			v := 130 + int(n)
			gray.SetRGBA(x, y, color.RGBA{R: uint8(v), G: uint8(v), B: uint8(v), A: 255})
		}
	}
	write("gray_day.jpg", gray)

	// blue_sky: clear-day vertical blue gradient, bright at horizon.
	blue := image.NewRGBA(image.Rect(0, 0, W, H))
	for y := 0; y < H; y++ {
		t := float64(y) / H // 0 zenith, 1 horizon
		r := uint8(60 + 100*t)
		g := uint8(120 + 80*t)
		b := uint8(230 + 20*t)
		for x := 0; x < W; x++ {
			blue.SetRGBA(x, y, color.RGBA{R: r, G: g, B: b, A: 255})
		}
	}
	write("blue_sky.jpg", blue)

	// orange_sunset: warm gradient — yellow-white at horizon through orange
	// to deep red-purple up top, with a cloud texture band.
	sunset := image.NewRGBA(image.Rect(0, 0, W, H))
	rnd = rand.New(rand.NewSource(7))
	for y := 0; y < H; y++ {
		t := float64(y) / H // 0 top, 1 horizon
		// top: (120,40,90) → horizon: (255,200,120)
		r := uint8(120 + 135*t)
		g := uint8(40 + 160*t)
		b := uint8(90 + 30*t)
		for x := 0; x < W; x++ {
			// cloud band in the middle third: brighten warm
			m := 1.0
			if t > 0.3 && t < 0.6 {
				m = 1.0 + 0.35*float64(rnd.Intn(3))/2
			}
			sunset.SetRGBA(x, y, color.RGBA{
				R: min255(float64(r) * m), G: min255(float64(g) * m), B: min255(float64(b) * m), A: 255})
		}
	}
	write("orange_sunset.jpg", sunset)

	// night_streetlight: near-black with scattered warm points.
	night := image.NewRGBA(image.Rect(0, 0, W, H))
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			night.SetRGBA(x, y, color.RGBA{R: 8, G: 8, B: 10, A: 255})
		}
	}
	for i := 0; i < 40; i++ {
		cx, cy := rnd.Intn(W), rnd.Intn(H)
		for dy := -2; dy <= 2; dy++ {
			for dx := -2; dx <= 2; dx++ {
				if dx*dx+dy*dy <= 4 {
					night.SetRGBA((cx+dx+W)%W, (cy+dy+H)%H, color.RGBA{R: 255, G: 190, B: 110, A: 255})
				}
			}
		}
	}
	write("night_streetlight.jpg", night)

	// offline: uniform dark slate ("camera unavailable" placeholder).
	off := image.NewRGBA(image.Rect(0, 0, W, H))
	for y := 0; y < H; y++ {
		for x := 0; x < W; x++ {
			off.SetRGBA(x, y, color.RGBA{R: 18, G: 22, B: 28, A: 255})
		}
	}
	write("offline.jpg", off)
}

func min255(v float64) uint8 {
	if v > 255 {
		return 255
	}
	return uint8(v)
}
