package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// NYCTMC API contract (frozen 2026-08-18 against
// testdata/providers/nyctmc_list.json): GET {base}/api/cameras returns
// [{"id","name","latitude","longitude","area","isOnline","imageUrl"}] where
// isOnline is the STRING "true"/"false". Images: {base}/api/cameras/{id}/image.
const NYCTMCBase = "https://webcams.nyctmc.org"
const NYCTMCUserAgent = "westward-sunset/1.0 (personal project)"

// CameraInfo is one entry of the DOT camera list.
type CameraInfo struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Area      string  `json:"area"`
	IsOnline  string  `json:"isOnline"`
	ImageURL  string  `json:"imageUrl"`
}

// FetchNYCTMCList downloads the full camera list.
func FetchNYCTMCList(ctx context.Context, baseURL, ua string) ([]CameraInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", baseURL+"/api/cameras", nil)
	if err != nil {
		return nil, err
	}
	if ua == "" {
		ua = NYCTMCUserAgent
	}
	req.Header.Set("User-Agent", ua)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("nyctmc list: status %d", resp.StatusCode)
	}
	var list []CameraInfo
	dec := json.NewDecoder(io.LimitReader(resp.Body, 16<<20))
	if err := dec.Decode(&list); err != nil {
		return nil, fmt.Errorf("nyctmc list: %w", err)
	}
	return list, nil
}

// ErrStale signals two consecutive 404s: the camera row should move to
// state=stale until a successful fetch or admin retry.
var ErrStale = errors.New("nyctmc: camera stale (two consecutive 404s)")

// NYCTMC wraps one DOT camera image endpoint with politeness rules:
// 15 s minimum interval, 403/429 exponential backoff 30 s..8 min.
type NYCTMC struct {
	base  string
	dotID string
	name  string
	ua    string
	lim   *Limiter
	h     *HTTP // shares all httpjpeg hardening; URL is the DOT image endpoint
}

// NewNYCTMC builds the adapter. dotID is the API uuid; the westward camera
// row stores it in ref.
func NewNYCTMC(cameraID, name, dotID string) *NYCTMC {
	base := NYCTMCBase
	imgURL := base + "/api/cameras/" + url.PathEscape(dotID) + "/image"
	h, _ := NewHTTP(HTTPConfig{
		ID:          cameraID,
		Name:        name,
		URL:         imgURL,
		Timeout:     10 * time.Second,
		MinInterval: 15 * time.Second,
		UserAgent:   NYCTMCUserAgent,
	})
	return &NYCTMC{base: base, dotID: dotID, name: name, ua: NYCTMCUserAgent, h: h}
}

func (n *NYCTMC) ID() string   { return n.h.ID() }
func (n *NYCTMC) Name() string { return n.name }

// backoff state for 403/429 (instance-local; enough for single process).
var (
	backoffMu    sync.Mutex
	backoffUntil map[string]time.Time
	notFound     map[string]int
)

func init() {
	backoffUntil = map[string]time.Time{}
	notFound = map[string]int{}
}

func (n *NYCTMC) Fetch(ctx context.Context) (Frame, error) {
	backoffMu.Lock()
	if until, ok := backoffUntil[n.h.ID()]; ok && time.Now().Before(until) {
		backoffMu.Unlock()
		return Frame{}, fmt.Errorf("nyctmc %s: backing off until %s", n.dotID, until.Format(time.RFC3339))
	}
	backoffMu.Unlock()

	frame, err := n.h.Fetch(ctx)
	if err == nil {
		backoffMu.Lock()
		notFound[n.h.ID()] = 0
		delete(backoffUntil, n.h.ID())
		backoffMu.Unlock()
		return frame, nil
	}
	var se *httpStatusError
	if errors.As(err, &se) {
		switch se.Code {
		case http.StatusForbidden, http.StatusTooManyRequests:
			backoffMu.Lock()
			cur := backoffUntil[n.h.ID()]
			next := time.Now().Add(nextBackoff(time.Until(cur)))
			backoffUntil[n.h.ID()] = next
			backoffMu.Unlock()
			return Frame{}, fmt.Errorf("nyctmc %s: rate limited, backoff until %s", n.dotID, next.Format(time.TimeOnly))
		case http.StatusNotFound:
			backoffMu.Lock()
			notFound[n.h.ID()]++
			stale := notFound[n.h.ID()] >= 2
			backoffMu.Unlock()
			if stale {
				return Frame{}, ErrStale
			}
			return Frame{}, err
		}
	}
	return Frame{}, err
}

// nextBackoff doubles the remaining backoff from 30 s, capped at 8 min.
func nextBackoff(remaining time.Duration) time.Duration {
	if remaining <= 0 {
		return 30 * time.Second
	}
	d := remaining * 2
	if d > 8*time.Minute {
		d = 8 * time.Minute
	}
	return d
}
