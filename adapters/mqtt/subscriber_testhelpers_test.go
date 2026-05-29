package mqtt

import (
	"context"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/outbox"
)

// This file has NO build tag so the shared subscriber test doubles compile into
// BOTH the unit (!integration) and integration test binaries. The disposition /
// metrics assertions in subscriber_test.go (unit) and integration_test.go
// (integration) both depend on recordingSettlement + recordingSubCollector.

// recordingSettlement records Commit / Release calls for assertions and can be
// configured to fail Commit.
type recordingSettlement struct {
	mu           sync.Mutex
	commitCalls  int
	releaseCalls int
	commitErr    error
}

func (r *recordingSettlement) Commit(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.commitCalls++
	return r.commitErr
}

func (r *recordingSettlement) Release(context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.releaseCalls++
	return nil
}

func (r *recordingSettlement) counts() (commit, release int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.commitCalls, r.releaseCalls
}

// ackHandler is a trivial SubscriberHandler that always Acks with no settlement.
// Used by tests that exercise Subscribe/Ready lifecycle and do not assert on the
// handler result itself.
func ackHandler(_ context.Context, _ outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
	return outbox.Ack(), nil
}

// recordingSubCollector records consume success / failure calls.
type recordingSubCollector struct {
	mu            sync.Mutex
	successCount  int
	failureCount  int
	lastReason    ConsumeFailureReason
	failureCounts map[ConsumeFailureReason]int
}

func newRecordingSubCollector() *recordingSubCollector {
	return &recordingSubCollector{failureCounts: make(map[ConsumeFailureReason]int)}
}

func (c *recordingSubCollector) RecordConsumeSuccess(_ context.Context, _ time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.successCount++
}

func (c *recordingSubCollector) RecordConsumeFailure(_ context.Context, reason ConsumeFailureReason) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failureCount++
	c.lastReason = reason
	c.failureCounts[reason]++
}

func (c *recordingSubCollector) snapshot() (success, failure int, last ConsumeFailureReason) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.successCount, c.failureCount, c.lastReason
}
