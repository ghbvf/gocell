package outbox

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestFailOpenTracker_TrippedOnHighRatio(t *testing.T) {
	tr := newFailOpenTracker(0.5)
	for i := 0; i < 5; i++ {
		tr.RecordSuccess()
	}
	for i := 0; i < 6; i++ {
		tr.RecordDrop()
	}
	// ratio = 6 / (5+6) = 0.545 > 0.5
	assert.True(t, tr.Tripped())
}

func TestFailOpenTracker_HealthyOnLowRatio(t *testing.T) {
	tr := newFailOpenTracker(0.05)
	for i := 0; i < 100; i++ {
		tr.RecordSuccess()
	}
	for i := 0; i < 2; i++ {
		tr.RecordDrop()
	}
	// ratio = 2/102 ≈ 0.0196 < 0.05
	assert.False(t, tr.Tripped())
}

func TestFailOpenTracker_NoEmitsBetweenChecks(t *testing.T) {
	tr := newFailOpenTracker(0.05)
	tr.RecordSuccess()
	tr.RecordDrop()
	_ = tr.Tripped() // first check sets baseline
	// no new emits
	assert.False(t, tr.Tripped(), "no new emits since last check should not trip")
}

func TestFailOpenTracker_ZeroThresholdDisabled(t *testing.T) {
	tr := newFailOpenTracker(0)
	for i := 0; i < 100; i++ {
		tr.RecordDrop()
	}
	assert.False(t, tr.Tripped(), "threshold=0 disables the tracker")
}

func TestFailOpenTracker_RecoveryAfterDropStop(t *testing.T) {
	tr := newFailOpenTracker(0.05)
	for i := 0; i < 10; i++ {
		tr.RecordDrop()
	}
	assert.True(t, tr.Tripped()) // baseline + degraded
	// recovery: no more drops, only success
	for i := 0; i < 100; i++ {
		tr.RecordSuccess()
	}
	assert.False(t, tr.Tripped(), "after drops stop and successes flow, tracker recovers")
}

func TestFailOpenTracker_NegativeOrInvalidThreshold(t *testing.T) {
	// Negative ratio is treated like 0 (disabled) — defensive
	tr := newFailOpenTracker(-0.1)
	for i := 0; i < 100; i++ {
		tr.RecordDrop()
	}
	assert.False(t, tr.Tripped(), "negative threshold should disable")

	// > 1.0 is allowed (effectively never trips since ratio ≤ 1.0)
	tr2 := newFailOpenTracker(1.5)
	for i := 0; i < 100; i++ {
		tr2.RecordDrop()
	}
	assert.False(t, tr2.Tripped(), "threshold > 1.0 effectively disables")
}
