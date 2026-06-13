package outbox

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	kout "github.com/ghbvf/gocell/kernel/outbox"
)

// panicRelayCollector is a test collector that panics on every call.
type panicRelayCollector struct{}

func (panicRelayCollector) RecordPollCycle(_ context.Context, _ kout.PollCycleResult) {
	panic("boom: poll cycle")
}

func (panicRelayCollector) RecordBatchSize(_ context.Context, _ int)    { panic("boom: batch size") }
func (panicRelayCollector) RecordReclaim(_ context.Context, _ int64)    { panic("boom: reclaim") }
func (panicRelayCollector) RecordCleanup(_ context.Context, _, _ int64) { panic("boom: cleanup") }

// mockCollector is a test collector that records calls without panicking.
type mockCollector struct {
	pollCycles    []kout.PollCycleResult
	batchSizes    []int
	reclaimCounts []int64
	cleanupCalls  []mockCleanupEntry
}

type mockCleanupEntry struct{ publishedDeleted, deadDeleted int64 }

func (m *mockCollector) RecordPollCycle(_ context.Context, r kout.PollCycleResult) {
	m.pollCycles = append(m.pollCycles, r)
}

func (m *mockCollector) RecordBatchSize(_ context.Context, size int) {
	m.batchSizes = append(m.batchSizes, size)
}

func (m *mockCollector) RecordReclaim(_ context.Context, count int64) {
	m.reclaimCounts = append(m.reclaimCounts, count)
}

func (m *mockCollector) RecordCleanup(_ context.Context, p, d int64) {
	m.cleanupCalls = append(m.cleanupCalls, mockCleanupEntry{p, d})
}

// typedNilCollector is a typed nil pointer for nil-dereference panic testing.
type typedNilCollector struct{}

func (t *typedNilCollector) RecordPollCycle(_ context.Context, _ kout.PollCycleResult) {
	panic("nil method called")
}

func (t *typedNilCollector) RecordBatchSize(_ context.Context, _ int) { panic("nil method called") }

func (t *typedNilCollector) RecordReclaim(_ context.Context, _ int64) { panic("nil method called") }

func (t *typedNilCollector) RecordCleanup(_ context.Context, _, _ int64) { panic("nil method called") }

func TestSafeRelayCollector_PanickingCollector_DoesNotCrash(t *testing.T) {
	ctx := context.Background()
	s := &safeRelayCollector{inner: panicRelayCollector{}}

	assert.NotPanics(t, func() {
		s.RecordPollCycle(ctx, kout.PollCycleResult{
			Event: kout.OutcomeCounts{Published: 1}, ClaimDur: time.Millisecond,
			PublishDur: time.Millisecond, WriteBackDur: time.Millisecond,
		})
	}, "RecordPollCycle panic must be recovered")

	assert.NotPanics(t, func() {
		s.RecordBatchSize(ctx, 10)
	}, "RecordBatchSize panic must be recovered")

	assert.NotPanics(t, func() {
		s.RecordReclaim(ctx, 5)
	}, "RecordReclaim panic must be recovered")

	assert.NotPanics(t, func() {
		s.RecordCleanup(ctx, 10, 3)
	}, "RecordCleanup panic must be recovered")
}

func TestSafeRelayCollector_TypedNil_DoesNotCrash(t *testing.T) {
	ctx := context.Background()
	var nilCollector *typedNilCollector // typed nil
	s := &safeRelayCollector{inner: nilCollector}

	assert.NotPanics(t, func() {
		s.RecordPollCycle(ctx, kout.PollCycleResult{Event: kout.OutcomeCounts{Published: 1}})
	}, "typed-nil collector must not crash")

	assert.NotPanics(t, func() {
		s.RecordBatchSize(ctx, 5)
	}, "typed-nil collector must not crash")

	assert.NotPanics(t, func() {
		s.RecordReclaim(ctx, 1)
	}, "typed-nil collector must not crash")

	assert.NotPanics(t, func() {
		s.RecordCleanup(ctx, 1, 0)
	}, "typed-nil collector must not crash")
}

func TestSafeRelayCollector_DelegatesCorrectly(t *testing.T) {
	ctx := context.Background()
	mc := &mockCollector{}
	s := &safeRelayCollector{inner: mc}

	s.RecordPollCycle(ctx, kout.PollCycleResult{Event: kout.OutcomeCounts{Published: 3}})
	s.RecordBatchSize(ctx, 42)
	s.RecordReclaim(ctx, 7)
	s.RecordCleanup(ctx, 10, 2)

	assert.Len(t, mc.pollCycles, 1)
	assert.Equal(t, 3, mc.pollCycles[0].Event.Published)

	assert.Len(t, mc.batchSizes, 1)
	assert.Equal(t, 42, mc.batchSizes[0])

	assert.Len(t, mc.reclaimCounts, 1)
	assert.Equal(t, int64(7), mc.reclaimCounts[0])

	assert.Len(t, mc.cleanupCalls, 1)
	assert.Equal(t, int64(10), mc.cleanupCalls[0].publishedDeleted)
	assert.Equal(t, int64(2), mc.cleanupCalls[0].deadDeleted)
}
