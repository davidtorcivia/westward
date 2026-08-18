// Package forecast implements sunset quality providers: Open-Meteo with
// the openmeteo-h1 heuristic, and SunsetHue. The engine's own solar
// computation is the canonical schedule; provider event times are stored
// for comparison only.
package forecast

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"time"
)

// Provider is one sunset-quality source.
type Provider interface {
	Name() string
	// SunsetForecast returns the provider's quality forecast for the local
	// date. EventUTC is the provider's sunset time (comparison only).
	SunsetForecast(ctx context.Context, lat, lon float64, localDate string) (Forecast, error)
}

// Forecast is the normalized result.
type Forecast struct {
	Provider       string
	EventUTC       time.Time
	Quality        float64 // 0..100
	Detail         string
	RawJSON        []byte
	AlgoVersion    string
	ComponentsJSON []byte // openmeteo: full heuristic breakdown; others: nil
}

// sharedClient: 10s timeout, 1 MiB body cap on every provider call.
func doJSON(ctx context.Context, req *http.Request) ([]byte, int, error) {
	req = req.Clone(ctx)
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return body, resp.StatusCode, nil
}

// Tuning are the openmeteo-h1 coefficients (runtime-editable).
type Tuning struct {
	Peak       float64 `json:"peak"`
	Width      float64 `json:"width"`
	LowPenalty float64 `json:"low_penalty"`
	HumPenalty float64 `json:"hum_penalty"`
	Overlap    float64 `json:"overlap"`
}

func (t Tuning) sanitized() Tuning {
	if t.Peak == 0 {
		t.Peak = 0.45
	}
	if t.Width == 0 {
		t.Width = 0.25
	}
	if t.LowPenalty == 0 {
		t.LowPenalty = 55
	}
	if t.HumPenalty == 0 {
		t.HumPenalty = 0.4
	}
	if t.Overlap == 0 {
		t.Overlap = 0.5
	}
	return t
}

// Components records every input and intermediate of openmeteo-h1 so the
// heuristic can be audited and retuned against observed outcomes later.
type Components struct {
	CCLow      float64 `json:"cc_low"`
	CCMid      float64 `json:"cc_mid"`
	CCHigh     float64 `json:"cc_high"`
	Visibility float64 `json:"visibility_m"`
	RH         float64 `json:"rh_pct"`
	MidHigh    float64 `json:"mid_high"`
	CloudScore float64 `json:"cloud_score"`
	LowPenalty float64 `json:"low_penalty"`
	VisBonus   float64 `json:"vis_bonus"`
	HumPenalty float64 `json:"hum_penalty"`
}

// HeuristicParts computes openmeteo-h1 and returns every component.
func HeuristicParts(ccLow, ccMid, ccHigh, visibility, rh float64, t Tuning) (Components, float64) {
	t = t.sanitized()
	mid, high, low := ccMid/100, ccHigh/100, ccLow/100
	midHigh := clamp01(math.Max(mid, high) + t.Overlap*math.Min(mid, high))
	cloudScore := 100 * math.Exp(-math.Pow(midHigh-t.Peak, 2)/(2*t.Width*t.Width))
	lowPenalty := t.LowPenalty * low
	visBonus := math.Max(0, math.Min(10, (visibility-10000)/2000))
	humPenalty := math.Max(0, rh-70) * t.HumPenalty
	c := Components{
		CCLow: ccLow, CCMid: ccMid, CCHigh: ccHigh,
		Visibility: visibility, RH: rh,
		MidHigh: midHigh, CloudScore: cloudScore,
		LowPenalty: lowPenalty, VisBonus: visBonus, HumPenalty: humPenalty,
	}
	return c, clamp(0, 100, cloudScore-lowPenalty+visBonus-humPenalty)
}

// Heuristic computes openmeteo-h1 quality from the hourly row nearest
// sunset. Inputs: cloud cover low/mid/high (0-100), visibility (m), rh (0-100).
func Heuristic(ccLow, ccMid, ccHigh, visibility, rh float64, t Tuning) float64 {
	_, q := HeuristicParts(ccLow, ccMid, ccHigh, visibility, rh, t)
	return q
}

func clamp01(v float64) float64 { return clamp(0, 1, v) }

func clamp(lo, hi, v float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// OpenMeteo implements Provider over api.open-meteo.com.
type OpenMeteo struct {
	BaseURL string // override for tests; default https://api.open-meteo.com
	Tuning  Tuning
	// Location to resolve the provider's local-time timestamps.
	Loc *time.Location
	// SunsetUTC picks the hourly row nearest sunset.
	SunsetUTC func(localDate string) (time.Time, error)
}

func (o *OpenMeteo) Name() string { return "openmeteo" }

// parseLocalHour accepts both "2006-01-02T15:04" and "2006-01-02 15:04".
func parseLocalHour(ts string, loc *time.Location) (time.Time, error) {
	if t, err := time.ParseInLocation("2006-01-02T15:04", ts, loc); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02 15:04", ts, loc)
}

type openMeteoResp struct {
	Hourly struct {
		Time   []string  `json:"time"`
		CCLow  []float64 `json:"cloud_cover_low"`
		CCMid  []float64 `json:"cloud_cover_mid"`
		CCHigh []float64 `json:"cloud_cover_high"`
		Vis    []float64 `json:"visibility"`
		RH     []float64 `json:"relative_humidity_2m"`
	} `json:"hourly"`
}

func (o *OpenMeteo) SunsetForecast(ctx context.Context, lat, lon float64, localDate string) (Forecast, error) {
	base := o.BaseURL
	if base == "" {
		base = "https://api.open-meteo.com"
	}
	url := fmt.Sprintf("%s/v1/forecast?latitude=%f&longitude=%f&hourly=cloud_cover_low,cloud_cover_mid,cloud_cover_high,visibility,relative_humidity_2m&timezone=auto&start_date=%s&end_date=%s",
		base, lat, lon, localDate, localDate)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return Forecast{}, err
	}
	raw, status, err := doJSON(ctx, req)
	if err != nil {
		return Forecast{}, err
	}
	if status != http.StatusOK {
		return Forecast{}, fmt.Errorf("openmeteo: status %d", status)
	}
	var resp openMeteoResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return Forecast{}, fmt.Errorf("openmeteo: %w", err)
	}
	h := resp.Hourly
	if len(h.Time) == 0 {
		return Forecast{}, fmt.Errorf("openmeteo: empty response")
	}

	// Target: nearest hourly row to sunset (caller supplies solar truth).
	var target time.Time
	if o.SunsetUTC != nil {
		target, err = o.SunsetUTC(localDate)
		if err != nil {
			return Forecast{}, err
		}
	} else {
		// Fallback: local noon of the date (tests without solar wiring).
		target, err = time.ParseInLocation("2006-01-02 15:04", localDate+" 18:00", o.Loc)
		if err != nil {
			return Forecast{}, err
		}
	}
	loc := o.Loc
	if loc == nil {
		loc = time.UTC
	}

	best := 0
	var bestDelta time.Duration = math.MaxInt64
	for i, ts := range h.Time {
		t, err := parseLocalHour(ts, loc)
		if err != nil {
			continue
		}
		d := t.Sub(target)
		if d < 0 {
			d = -d
		}
		if d < bestDelta && i < len(h.CCMid) {
			bestDelta = d
			best = i
		}
	}
	if best >= len(h.CCLow) || best >= len(h.CCHigh) || best >= len(h.Vis) || best >= len(h.RH) {
		return Forecast{}, fmt.Errorf("openmeteo: hourly arrays length mismatch")
	}
	comps, q := HeuristicParts(h.CCLow[best], h.CCMid[best], h.CCHigh[best], h.Vis[best], h.RH[best], o.Tuning)
	compsJSON, _ := json.Marshal(comps)
	return Forecast{
		Provider:       "openmeteo",
		EventUTC:       target.UTC(),
		Quality:        math.Round(q*10) / 10,
		Detail:         fmt.Sprintf("cc low/mid/high %.0f/%.0f/%.0f, vis %.0fm, rh %.0f%%", h.CCLow[best], h.CCMid[best], h.CCHigh[best], h.Vis[best], h.RH[best]),
		RawJSON:        raw,
		AlgoVersion:    "openmeteo-h1",
		ComponentsJSON: compsJSON,
	}, nil
}

// SunsetHue implements Provider over api.sunsethue.com. Contract frozen
// against testdata/providers/sunsethue.json: fields nested under "data",
// quality 0..1, quality_text, time as UTC ISO.
type SunsetHue struct {
	APIKey  string
	BaseURL string
}

func (s *SunsetHue) Name() string { return "sunsethue" }

type sunsetHueResp struct {
	Data struct {
		Quality     float64 `json:"quality"`
		QualityText string  `json:"quality_text"`
		Time        string  `json:"time"`
	} `json:"data"`
}

func (s *SunsetHue) SunsetForecast(ctx context.Context, lat, lon float64, localDate string) (Forecast, error) {
	if s.APIKey == "" {
		return Forecast{}, fmt.Errorf("sunsethue: no API key")
	}
	base := s.BaseURL
	if base == "" {
		base = "https://api.sunsethue.com"
	}
	url := fmt.Sprintf("%s/event?latitude=%f&longitude=%f&date=%s&type=sunset", base, lat, lon, localDate)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return Forecast{}, err
	}
	req.Header.Set("x-api-key", s.APIKey)
	raw, status, err := doJSON(ctx, req)
	if err != nil {
		return Forecast{}, err
	}
	if status != http.StatusOK {
		return Forecast{}, fmt.Errorf("sunsethue: status %d", status)
	}
	var resp sunsetHueResp
	if err := json.Unmarshal(raw, &resp); err != nil {
		return Forecast{}, fmt.Errorf("sunsethue: %w", err)
	}
	ev, err := time.Parse(time.RFC3339, resp.Data.Time)
	if err != nil {
		return Forecast{}, fmt.Errorf("sunsethue: bad time %q", resp.Data.Time)
	}
	q := clamp(0, 1, resp.Data.Quality) * 100
	return Forecast{
		Provider:    "sunsethue",
		EventUTC:    ev.UTC(),
		Quality:     math.Round(q*10) / 10,
		Detail:      resp.Data.QualityText,
		RawJSON:     raw,
		AlgoVersion: "sunsethue-1",
	}, nil
}
