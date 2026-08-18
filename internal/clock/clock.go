// Package clock provides an injectable clock so all timing logic in the
// engine and scheduler is testable with a fake clock.
package clock

import (
	"sort"
	"sync"
	"time"
)

// Clock is the minimal surface the engine needs. Scheduling is expressed as
// "sleep until next event, then recompute", so After covers timers too.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

// Real is the wall clock.
type Real struct{}

func (Real) Now() time.Time                { return time.Now() }
func (Real) After(d time.Duration) <-chan time.Time { return time.After(d) }

// Fake is a manually advanced clock for tests. After channels fire in
// chronological order when Advance is called; simultaneous timers fire in
// registration order.
type Fake struct {
	mu    sync.Mutex
	now   time.Time
	timer seq // monotonic registration sequence for stable ordering
	// firing is closed (fired) once per waiter when its deadline passes.
	pending []fakeWaiter
}

type fakeWaiter struct {
	deadline time.Time
	seq      uint64
	ch       chan time.Time
}

type seq struct{ n uint64 }

func (s *seq) next() uint64 { s.n++; return s.n }

// NewFake returns a fake clock pinned at t.
func NewFake(t time.Time) *Fake { return &Fake{now: t} }

func (f *Fake) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *Fake) After(d time.Duration) <-chan time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := make(chan time.Time, 1)
	f.pending = append(f.pending, fakeWaiter{deadline: f.now.Add(d), seq: f.timer.next(), ch: ch})
	return ch
}

// Advance moves the clock forward by d, firing every pending After whose
// deadline has been reached.
func (f *Fake) Advance(d time.Duration) {
	f.mu.Lock()
	target := f.now.Add(d)
	var due []fakeWaiter
	kept := f.pending[:0]
	for _, w := range f.pending {
		if !w.deadline.After(target) {
			due = append(due, w)
		} else {
			kept = append(kept, w)
		}
	}
	f.pending = kept
	f.now = target
	sort.Slice(due, func(i, j int) bool { return due[i].seq < due[j].seq })
	f.mu.Unlock()
	for _, w := range due {
		w.ch <- w.deadline
	}
}
