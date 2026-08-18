// Package engine runs the daily schedule: forecast, capture windows,
// GO trigger, finalization, retention. Trigger evaluation is pure and
// table-tested; orchestration lives in engine.go.
package engine

import (
	"math"
	"sort"
	"time"
)

// Sample is one scored frame in a capture run.
type Sample struct {
	T     time.Time
	Score float64
}

// TriggerParams are the four §8.3 gates, per camera after defaults+overrides.
type TriggerParams struct {
	ThresholdAbs float64
	Ratio        float64
	DeltaAbs     float64
	RiseDelta    float64
}

// Defaults from settings (plan §16).
var DefaultParams = TriggerParams{ThresholdAbs: 12.0, Ratio: 1.6, DeltaAbs: 4.0, RiseDelta: 1.5}

// minSamples arms the trigger; baseline is median(first 5).
const minSamples = 5

// TriggerState carries one camera's evaluation history across a run.
// Duplicate frames never advance it (caller skips those before Append).
type TriggerState struct {
	samples    []Sample
	interval   time.Duration // for the sample-gap constraint
	fired      bool
	Components TriggerComponents
}

// TriggerComponents records the four values at fire time for diagnosis.
type TriggerComponents struct {
	Recent, Baseline, Prior float64
	Samples                 int
}

func NewTriggerState(interval time.Duration) *TriggerState {
	return &TriggerState{interval: interval}
}

// Append adds a valid, non-duplicate sample.
func (ts *TriggerState) Append(t time.Time, score float64) {
	ts.samples = append(ts.samples, Sample{t, score})
}

func (ts *TriggerState) Fired() bool { return ts.fired }

// Evaluate applies all four gates. Returns fire=true exactly once; after a
// fire (or once fired), further evaluation is a no-op.
func (ts *TriggerState) Evaluate(p TriggerParams) bool {
	if ts.fired || len(ts.samples) < minSamples {
		return false
	}
	// Sample-gap constraint: the last 3 samples must each be ≤ 2× interval
	// apart, else recency is broken; wait for fresh consecutive samples.
	n := len(ts.samples)
	for i := n - 2; i >= n-3 && i >= 1; i-- {
		if ts.samples[i].T.Sub(ts.samples[i-1].T) > 2*ts.interval {
			return false
		}
	}
	baseline := medianScores(ts.samples[:minSamples])
	recent := medianScores(ts.samples[n-3:])
	var prior float64
	if n >= 6 {
		prior = medianScores(ts.samples[n-6 : n-3])
	} else {
		prior = baseline // not enough history; prior = baseline (stricter rise gate)
	}
	fire := recent >= p.ThresholdAbs &&
		recent >= baseline*p.Ratio &&
		recent-baseline >= p.DeltaAbs &&
		recent-prior >= p.RiseDelta
	if fire {
		ts.fired = true
		ts.Components = TriggerComponents{recent, baseline, prior, n}
	}
	return fire
}

func medianScores(s []Sample) float64 {
	v := make([]float64, len(s))
	for i, x := range s {
		v[i] = x.Score
	}
	sort.Float64s(v)
	m := len(v) / 2
	if len(v)%2 == 1 {
		return v[m]
	}
	return (v[m-1] + v[m]) / 2
}

// round2 matches the stored 2-dp convention.
func round2(v float64) float64 { return math.Round(v*100) / 100 }
