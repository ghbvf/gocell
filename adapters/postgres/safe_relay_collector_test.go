package postgres

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/stretchr/testify/assert"
)

// panicRelayCollector is a test collector that panics on every call.
type panicRelayCollector struct{}

func (panicRelayCollector) RecordPollCycle(_ outbox.PollCycleResult) { panic("boom: poll cycle") }
func (panicRelayCollector) RecordBatchSize(_ int)                    { panic("boom: batch size") }
func (panicRelayCollector) RecordReclaim(_ int64)                    { panic("boom: reclaim") }
func (panicRelayCollector) RecordCleanup(_, _ int64)                 { panic("boom: cleanup") }

func TestSafeRelayCollector_PanickingCollector_DoesNotCrash(t *testing.T) {
	s := &safeRelayCollector{inner: panicRelayCollector{}}

	// None of these should panic — all should be silently recovered.
	assert.NotPanics(t, func() {
		s.RecordPollCycle(outbox.PollCycleResult{
			Published: 1, ClaimDur: time.Millisecond,
			PublishDur: time.Millisecond, WriteBackDur: time.Millisecond,
		})
	}, "RecordPollCycle panic must be recovered")

	assert.NotPanics(t, func() {
		s.RecordBatchSize(10)
	}, "RecordBatchSize panic must be recovered")

	assert.NotPanics(t, func() {
		s.RecordReclaim(5)
	}, "RecordReclaim panic must be recovered")

	assert.NotPanics(t, func() {
		s.RecordCleanup(10, 3)
	}, "RecordCleanup panic must be recovered")
}

func TestSafeRelayCollector_TypedNil_DoesNotCrash(t *testing.T) {
	// A typed nil: interface holds a nil *mockRelayCollector pointer.
	// Calling methods on it will panic with nil pointer dereference.
	var nilCollector *mockRelayCollector // typed nil
	s := &safeRelayCollector{inner: nilCollector}

	assert.NotPanics(t, func() {
		s.RecordPollCycle(outbox.PollCycleResult{Published: 1})
	}, "typed-nil collector must not crash")

	assert.NotPanics(t, func() {
		s.RecordBatchSize(5)
	}, "typed-nil collector must not crash")

	assert.NotPanics(t, func() {
		s.RecordReclaim(1)
	}, "typed-nil collector must not crash")

	assert.NotPanics(t, func() {
		s.RecordCleanup(1, 0)
	}, "typed-nil collector must not crash")
}

func TestSafeRelayCollector_DelegatesCorrectly(t *testing.T) {
	mc := &mockRelayCollector{}
	s := &safeRelayCollector{inner: mc}

	s.RecordPollCycle(outbox.PollCycleResult{Published: 3})
	s.RecordBatchSize(42)
	s.RecordReclaim(7)
	s.RecordCleanup(10, 2)

	assert.Len(t, mc.pollCycles, 1)
	assert.Equal(t, 3, mc.pollCycles[0].Published)

	assert.Len(t, mc.batchSizes, 1)
	assert.Equal(t, 42, mc.batchSizes[0])

	assert.Len(t, mc.reclaimCounts, 1)
	assert.Equal(t, int64(7), mc.reclaimCounts[0])

	assert.Len(t, mc.cleanupCalls, 1)
	assert.Equal(t, int64(10), mc.cleanupCalls[0].publishedDeleted)
	assert.Equal(t, int64(2), mc.cleanupCalls[0].deadDeleted)
}
