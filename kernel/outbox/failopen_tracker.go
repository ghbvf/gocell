package outbox

import "sync"

// failOpenTracker tracks the ratio of fail-open dropped emits to total emits.
// It is checked from /readyz to surface "outbox is in fail-open path losing
// events" as a soft-failure (degraded) signal without dependence on time
// constants — the implicit window is the interval between two consecutive
// /readyz probes (typically 10-30s under K8s readinessProbe), so the tracker
// adapts naturally to probe cadence and emit volume.
//
// Algorithm: maintain monotonic totalEmits + droppedEmits counters; on each
// Tripped() call, compute ratio = (deltaDropped / deltaTotal) since the last
// call and compare against the configured threshold. With no new emits between
// calls, return false (no signal to evaluate; preserve healthy state).
//
// ref: prometheus increase()/rate() — semantically "delta per scrape window";
// we use the /readyz probe cadence as the implicit window.
type failOpenTracker struct {
	mu             sync.Mutex
	totalEmits     uint64
	droppedEmits   uint64
	lastTotal      uint64
	lastDropped    uint64
	thresholdRatio float64 // 0 or negative disables the tracker (always returns false)
}

func newFailOpenTracker(thresholdRatio float64) *failOpenTracker {
	return &failOpenTracker{thresholdRatio: thresholdRatio}
}

// RecordSuccess increments totalEmits.
func (t *failOpenTracker) RecordSuccess() {
	t.mu.Lock()
	t.totalEmits++
	t.mu.Unlock()
}

// RecordDrop increments BOTH totalEmits and droppedEmits, so totalEmits is
// the denominator of every emit attempt — successful + dropped.
func (t *failOpenTracker) RecordDrop() {
	t.mu.Lock()
	t.totalEmits++
	t.droppedEmits++
	t.mu.Unlock()
}

// Tripped returns true if the drop ratio since the last call exceeds the
// configured threshold. If no emits occurred since the last call (deltaTotal
// == 0), returns false (preserve healthy state).
//
// Side effect: updates lastTotal/lastDropped to current values.
func (t *failOpenTracker) Tripped() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.thresholdRatio <= 0 {
		return false
	}

	deltaTotal := t.totalEmits - t.lastTotal
	deltaDropped := t.droppedEmits - t.lastDropped

	t.lastTotal = t.totalEmits
	t.lastDropped = t.droppedEmits

	if deltaTotal == 0 {
		return false
	}

	ratio := float64(deltaDropped) / float64(deltaTotal)
	return ratio > t.thresholdRatio
}
