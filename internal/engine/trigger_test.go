package engine

import (
	"testing"
	"time"
)

var base = time.Date(2026, 8, 19, 23, 0, 0, 0, time.UTC)

func seq(start float64, step float64, n int, interval time.Duration) *TriggerState {
	ts := NewTriggerState(interval)
	for i := 0; i < n; i++ {
		ts.Append(base.Add(time.Duration(i)*interval), start+float64(i)*step)
	}
	return ts
}

// Table tests, plan §14 case 3.
func TestTriggerTable(t *testing.T) {
	p := DefaultParams
	const iv = 45 * time.Second

	cases := []struct {
		name string
		ts   func() *TriggerState
		fire bool
	}{
		{"flat low", func() *TriggerState { return seq(2, 0.1, 12, iv) }, false},
		{"clean rise", func() *TriggerState { return seq(3, 2.5, 12, iv) }, true},
		// Noise jitter around a rising trend: median smoothing must fire.
		{"noise rise", func() *TriggerState {
			ts := NewTriggerState(iv)
			jitter := []float64{0, 3, 2, 4, 1, 5, 2, 6, 3, 8, 4, 9, 6, 10}
			for i, j := range jitter {
				ts.Append(base.Add(time.Duration(i)*iv), 3+float64(i)*2.0+j)
			}
			return ts
		}, true},
		// Single spike then fall: medians suppress it.
		{"spike fall", func() *TriggerState {
			ts := NewTriggerState(iv)
			scores := []float64{3, 3.2, 2.9, 3.1, 3.0, 3.2, 40, 2.8, 3.0, 2.9}
			for i, s := range scores {
				ts.Append(base.Add(time.Duration(i)*iv), s)
			}
			return ts
		}, false},
		// Warm idle: high absolute score but flat — ratio gate holds it back.
		{"warm idle", func() *TriggerState {
			ts := NewTriggerState(iv)
			for i := 0; i < 12; i++ {
				ts.Append(base.Add(time.Duration(i)*iv), 30+float64(i%3)*0.5)
			}
			return ts
		}, false},
		// Under 5 samples: never fires even with a perfect rise.
		{"too few samples", func() *TriggerState { return seq(3, 10, 4, iv) }, false},
		// Exactly 5 samples, strongly rising: fires (prior falls back to
		// baseline). baseline=3, recent=20 clears all four gates.
		{"exactly five rising", func() *TriggerState {
			ts := NewTriggerState(iv)
			for i, s := range []float64{1, 2, 3, 20, 30} {
				ts.Append(base.Add(time.Duration(i)*iv), s)
			}
			return ts
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.ts().Evaluate(p)
			if got != c.fire {
				t.Fatalf("fire = %v, want %v", got, c.fire)
			}
		})
	}
}

// Gap-broken sequence: a >2× interval hole in the last 3 gaps blocks fire.
func TestGapBroken(t *testing.T) {
	const iv = 45 * time.Second
	ts := NewTriggerState(iv)
	scores := []float64{3, 5, 7, 9, 11, 13, 15, 17, 19, 21}
	for i, s := range scores {
		at := base.Add(time.Duration(i) * iv)
		if i == 7 { // gap after this sample
			at = at.Add(10 * iv)
		}
		ts.Append(at, s)
	}
	if ts.Evaluate(DefaultParams) {
		t.Fatal("fired despite broken recency")
	}
	// Fresh consecutive samples restore evaluation.
	ts.Append(ts.samples[len(ts.samples)-1].T.Add(iv), 23)
	ts.Append(ts.samples[len(ts.samples)-1].T.Add(iv), 25)
	if !ts.Evaluate(DefaultParams) {
		t.Fatal("did not fire after recency restored")
	}
}

// Duplicates never advance history (engine skips them before Append); here
// we verify the state machine honors fired-once semantics.
func TestFiresOnce(t *testing.T) {
	const iv = 45 * time.Second
	ts := seq(3, 3, 12, iv)
	if !ts.Evaluate(DefaultParams) {
		t.Fatal("should fire")
	}
	ts.Append(base.Add(20*iv), 90)
	if ts.Evaluate(DefaultParams) {
		t.Fatal("fired twice")
	}
	if ts.Fired() != true {
		t.Fatal("Fired() state lost")
	}
}

func TestComponentsRecorded(t *testing.T) {
	const iv = 45 * time.Second
	ts := seq(3, 3, 12, iv)
	ts.Evaluate(DefaultParams)
	c := ts.Components
	if c.Samples != 12 || c.Baseline == 0 || c.Recent <= c.Baseline {
		t.Fatalf("components: %+v", c)
	}
}
