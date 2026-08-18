package score

import (
	"image/jpeg"
	"os"
	"testing"
)

// Frozen fixture goldens, recorded 2026-08-18 from the committed synthetic
// skies under testdata/frames/ (generator: gen/main.go). Tolerance ±0.05:
// same-build determinism is exact; the small slack tolerates decoder float
// differences across Go patch releases. Any change beyond that is a math
// change requiring a scoring version bump.
func TestFixtureGoldens(t *testing.T) {
	cases := []struct {
		name        string
		wantScore   float64
		wantFrac    float64
		wantMedianL float64
	}{
		{"gray_day", 0.00, 0.000, 54.4},
		{"blue_sky", 0.00, 0.000, 57.9},
		{"orange_sunset", 47.12, 1.000, 41.7},
		{"night_streetlight", 0.05, 0.002, 0.0},
		{"offline", 0.00, 0.000, 0.0},
	}
	scores := map[string]float64{}
	for _, c := range cases {
		f, err := os.Open("../../testdata/frames/" + c.name + ".jpg")
		if err != nil {
			t.Fatal(err)
		}
		img, err := jpeg.Decode(f)
		f.Close()
		if err != nil {
			t.Fatal(c.name, err)
		}
		res, err := Score(img, nil)
		if err != nil {
			t.Fatal(c.name, err)
		}
		if diff := abs(res.Score - c.wantScore); diff > 0.05 {
			t.Errorf("%s score = %.2f, want %.2f", c.name, res.Score, c.wantScore)
		}
		if diff := abs(res.SunsetPixelFraction - c.wantFrac); diff > 0.01 {
			t.Errorf("%s fraction = %.3f, want %.3f", c.name, res.SunsetPixelFraction, c.wantFrac)
		}
		if diff := abs(res.MedianL - c.wantMedianL); diff > 0.5 {
			t.Errorf("%s medianL = %.1f, want %.1f", c.name, res.MedianL, c.wantMedianL)
		}
		if res.ScoringVersion != ScoringVersion {
			t.Errorf("%s scoring version = %q", c.name, res.ScoringVersion)
		}
		scores[c.name] = res.Score
	}

	// Plan §14 case 2 ordering and thresholds.
	if scores["orange_sunset"] <= 25 {
		t.Error("orange fixture should exceed 25")
	}
	if scores["gray_day"] >= 4 || scores["blue_sky"] >= 4 {
		t.Error("gray/blue fixtures should stay under 4")
	}
	if scores["orange_sunset"] <= scores["blue_sky"] || scores["orange_sunset"] <= scores["gray_day"] {
		t.Error("ordering orange >> blue ≈ gray violated")
	}
	// Streetlight fixture is dominated by L* < 12 pixels: it fails the
	// archive darkness floor (median_L >= 10) even though a few warm points
	// score. The engine applies that floor; here we assert the median proves it.
	if scores["night_streetlight"] > scores["gray_day"] {
		// The raw score may sit marginally above the (zero) gray score; the
		// darkness floor is what excludes it. That contract is tested at the
		// engine level; this guard documents the relationship.
		t.Logf("night score %.2f vs gray %.2f: darkness floor handles exclusion", scores["night_streetlight"], scores["gray_day"])
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
