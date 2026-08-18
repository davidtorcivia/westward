package forecast

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

func defTuning() Tuning {
	return Tuning{Peak: 0.45, Width: 0.25, LowPenalty: 55, HumPenalty: 0.4, Overlap: 0.5}
}

// Plan §9.2 table with ±1 tolerance.
func TestHeuristicTable(t *testing.T) {
	cases := []struct {
		name                    string
		low, mid, high, vis, rh float64
		want                    float64
	}{
		{"clear dry", 0, 0, 0, 20000, 40, 24.8},        // cloud 19.8 + visBonus 5
		{"ideal mid clouds", 0, 45, 0, 20000, 40, 100}, // cloudScore 100
		{"heavy low deck", 90, 0, 0, 20000, 40, 0},     // 100*exp(-(0-0.45)²/0.125)=100*exp(-1.62)≈19.8 - 49.5 → 0
		{"overcast mid", 0, 95, 0, 20000, 40, 18.5},    // cloud 13.5 + visBonus 5
		{"humid", 0, 45, 0, 20000, 90, 97},             // 100 - 8 + visBonus 5
		{"low vis capped", 0, 45, 0, 60000, 40, 100},   // visBonus caps at 10
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := Heuristic(c.low, c.mid, c.high, c.vis, c.rh, defTuning())
			if got < c.want-1 || got > c.want+1 {
				t.Errorf("Heuristic(%s) = %.2f, want %.2f ±1", c.name, got, c.want)
			}
		})
	}
}

func TestHeuristicClampsAndOverlap(t *testing.T) {
	// Output always within [0,100] even for absurd inputs.
	if q := Heuristic(100, 100, 100, 0, 100, defTuning()); q < 0 || q > 100 {
		t.Errorf("clamp failed: %v", q)
	}
	// Overlap-aware union: mid=high=0.45 must NOT double-count to 0.9.
	a := Heuristic(0, 45, 45, 20000, 40, defTuning()) // mid_high = 0.45+0.225=0.675
	b := Heuristic(0, 68, 0, 20000, 40, defTuning())  // single layer 0.68
	if a < b-25 {                                     // union of two half-layers scores near a single thicker layer
		t.Errorf("overlap union too punitive: %.1f vs %.1f", a, b)
	}
}

// Contract test against the frozen Open-Meteo fixture (Phase 0 capture).
func TestOpenMeteoContractFixture(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/providers/openmeteo.json")
	if err != nil {
		t.Fatal(err)
	}
	var resp openMeteoResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	h := resp.Hourly
	if len(h.Time) == 0 || len(h.Time) != len(h.CCLow) || len(h.Time) != len(h.CCMid) ||
		len(h.Time) != len(h.CCHigh) || len(h.Time) != len(h.Vis) || len(h.Time) != len(h.RH) {
		t.Fatalf("fixture shape mismatch: %d rows", len(h.Time))
	}
	// All timestamps parse in the configured location (local time).
	loc, _ := time.LoadLocation("America/New_York")
	for _, ts := range h.Time {
		if _, err := parseLocalHour(ts, loc); err != nil {
			t.Fatalf("bad local timestamp %q: %v", ts, err)
		}
	}
}

// End-to-end against a local server serving the frozen fixture.
func TestOpenMeteoFromFixtureServer(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/providers/openmeteo.json")
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("timezone") == "" || r.URL.Query().Get("start_date") == "" {
			t.Errorf("missing query params: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write(raw)
	}))
	defer ts.Close()

	loc, _ := time.LoadLocation("America/New_York")
	om := &OpenMeteo{
		BaseURL: ts.URL, Tuning: defTuning(), Loc: loc,
		SunsetUTC: func(string) (time.Time, error) {
			// Fixture was captured 2026-08-18; sunset ~19:48 local.
			return time.Date(2026, 8, 18, 19, 48, 0, 0, loc), nil
		},
	}
	f, err := om.SunsetForecast(context.Background(), 40.6782, -73.9442, "2026-08-18")
	if err != nil {
		t.Fatal(err)
	}
	if f.Provider != "openmeteo" || f.AlgoVersion != "openmeteo-h1" {
		t.Fatalf("provider/version: %s/%s", f.Provider, f.AlgoVersion)
	}
	if f.Quality < 0 || f.Quality > 100 {
		t.Fatalf("quality out of range: %v", f.Quality)
	}
	if len(f.RawJSON) == 0 || f.Detail == "" {
		t.Fatal("raw/detail missing")
	}
}

// Contract test against the frozen SunsetHue fixture (Phase 0 capture):
// data.quality 0.46 "Good", data.time UTC ISO.
func TestSunsetHueContractFixture(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/providers/sunsethue.json")
	if err != nil {
		t.Fatal(err)
	}
	var resp sunsetHueResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Quality != 0.46 || resp.Data.QualityText != "Good" {
		t.Fatalf("fixture drifted: %+v", resp.Data)
	}
	ev, err := time.Parse(time.RFC3339, resp.Data.Time)
	if err != nil || ev.IsZero() {
		t.Fatalf("event time: %q %v", resp.Data.Time, err)
	}

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("x-api-key") != "test-key" {
			w.WriteHeader(401)
			return
		}
		w.Write(raw)
	}))
	defer ts.Close()
	sh := &SunsetHue{APIKey: "test-key", BaseURL: ts.URL}
	f, err := sh.SunsetForecast(context.Background(), 40.6782, -73.9442, "2026-08-19")
	if err != nil {
		t.Fatal(err)
	}
	if f.Quality != 46 || f.Detail != "Good" || f.Provider != "sunsethue" {
		t.Fatalf("forecast: %+v", f)
	}
}

func TestSunsetHueMissingKey(t *testing.T) {
	sh := &SunsetHue{}
	if _, err := sh.SunsetForecast(context.Background(), 1, 2, "2026-08-19"); err == nil {
		t.Fatal("no key accepted")
	}
}

func TestProviderErrorStatuses(t *testing.T) {
	for _, code := range []int{500, 429} {
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(code)
		}))
		om := &OpenMeteo{BaseURL: ts.URL, Tuning: defTuning()}
		if _, err := om.SunsetForecast(context.Background(), 1, 2, "2026-08-19"); err == nil {
			t.Fatalf("openmeteo %d accepted", code)
		}
		sh := &SunsetHue{APIKey: "k", BaseURL: ts.URL}
		if _, err := sh.SunsetForecast(context.Background(), 1, 2, "2026-08-19"); err == nil {
			t.Fatalf("sunsethue %d accepted", code)
		}
		ts.Close()
	}
}
