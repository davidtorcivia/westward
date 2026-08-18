// Package health carries the engine heartbeat shared by /livez and the engine.
package health

import (
	"sync/atomic"
	"time"
)

// Heartbeat is a last-beat timestamp. The engine loop beats every cycle;
// /livez reports unhealthy when the loop has been silent past a threshold.
type Heartbeat struct {
	lastMS atomic.Int64
}

func (h *Heartbeat) Beat() { h.lastMS.Store(time.Now().UnixMilli()) }
func (h *Heartbeat) Healthy(max time.Duration) bool {
	last := h.lastMS.Load()
	return last != 0 && time.Since(time.UnixMilli(last)) <= max
}
