package solar

import (
	"testing"
	"time"
	_ "time/tzdata"
)

func mustLoc(t *testing.T) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	return loc
}

// Golden values for Brooklyn (40.6782, -73.9442), both 2026 DST transitions
// and both solstices. Sunset/civil dusk verified against NOAA solar
// calculator at capture time; ±2 min tolerance absorbs model differences.
func TestGoldens(t *testing.T) {
	loc := mustLoc(t)
	cases := []struct {
		name string
		date string
		// expected local wall-clock times
		sunset string
		dusk   string
	}{
		{"dst-spring-forward", "2026-03-08", "18:55", "19:22"},
		{"day-after-spring", "2026-03-09", "18:55", "19:23"},
		{"summer-solstice", "2026-06-21", "20:31", "21:04"},
		{"dst-fall-back", "2026-11-01", "16:51", "17:20"},
		{"winter-solstice", "2026-12-21", "16:32", "17:02"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			day, _ := time.ParseInLocation("2006-01-02", c.date, loc)
			ev, err := For(loc, 40.6782, -73.9442, day)
			if err != nil {
				t.Fatal(err)
			}
			wantSS, _ := time.ParseInLocation("2006-01-02 15:04", c.date+" "+c.sunset, loc)
			wantDusk, _ := time.ParseInLocation("2006-01-02 15:04", c.date+" "+c.dusk, loc)
			if diff := ev.Sunset.Sub(wantSS); diff < -2*time.Minute || diff > 2*time.Minute {
				t.Errorf("sunset %s, want %s (±2m)", ev.Sunset.In(loc).Format("15:04"), c.sunset)
			}
			if diff := ev.Dusk.Sub(wantDusk); diff < -2*time.Minute || diff > 2*time.Minute {
				t.Errorf("dusk %s, want %s (±2m)", ev.Dusk.In(loc).Format("15:04"), c.dusk)
			}
		})
	}
}

// DST spring-forward day: the local wall-clock sunset jumps ~1h vs the day
// before (absolute time only drifts ~1 min/day; the jump is a zone effect).
func TestDSTJump(t *testing.T) {
	loc := mustLoc(t)
	tod := func(ts time.Time) time.Duration {
		ts = ts.In(loc)
		return time.Duration(ts.Hour())*time.Hour + time.Duration(ts.Minute())*time.Minute
	}
	d1, _ := time.ParseInLocation("2006-01-02", "2026-03-07", loc)
	d2, _ := time.ParseInLocation("2006-01-02", "2026-03-08", loc)
	e1, err := For(loc, 40.6782, -73.9442, d1)
	if err != nil {
		t.Fatal(err)
	}
	e2, err := For(loc, 40.6782, -73.9442, d2)
	if err != nil {
		t.Fatal(err)
	}
	jump := tod(e2.Sunset) - tod(e1.Sunset)
	if jump < 55*time.Minute || jump > 65*time.Minute {
		t.Errorf("wall-clock sunset jump over spring-forward = %v, want ~1h", jump)
	}
}
