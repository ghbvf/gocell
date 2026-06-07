package mqtt

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// mustNewEntry builds a sealed outbox.Entry via the producer constructor
// (outbox.Entry is sealed-construction since #1229 — struct literals no longer
// compile). Mirrors adapters/rabbitmq's test helper. NewEntry stamps
// createdAt / occurredAt from the clock; tests override identity via WithID /
// WithTopic.
func mustNewEntry(t testing.TB, eventType string, payload []byte, opts ...outbox.EntryOption) outbox.Entry {
	t.Helper()
	e, err := outbox.NewEntry(clock.Real(), context.Background(), eventType, payload, opts...)
	if err != nil {
		t.Fatalf("outbox.NewEntry: %v", err)
	}
	return e
}

// This file has NO build tag so the shared subscriber test doubles compile into
// BOTH the unit (!integration) and integration test binaries. The disposition /
// metrics assertions in subscriber_test.go (unit) and integration_test.go
// (integration) both depend on recordingSettlement + recordingSubCollector.
//
// Unit-only dispatchAck white-box doubles (subAckRecorder / fakeAckConn /
// newDispatchAckSubscriber / dispatchAckEntry / ackHandler) live in
// subscriber_dispatchack_test.go (//go:build !integration) — they have no
// caller in the integration build and would report `unused` if compiled there.

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

// recordingSubCollector records consume success / failure / dead-letter
// (capture + failure) calls, plus the in-flight gauge delta stream.
type recordingSubCollector struct {
	mu              sync.Mutex
	successCount    int
	failureCount    int
	deadLetterCnt   int
	deadLetterFailN int
	lastReason      ConsumeFailureReason
	failureCounts   map[ConsumeFailureReason]int
	dlxCounts       map[ConsumeFailureReason]int
	dlxFailedCounts map[ConsumeFailureReason]int
	// inflight gauge delta stream: AdjustInflight appends each delta; inflightNet
	// is the running sum (current gauge value), inflightPeak the max running sum
	// observed (== concurrent in-flight deliveries at peak).
	inflightDeltas []int64
	inflightNet    int64
	inflightPeak   int64
}

func newRecordingSubCollector() *recordingSubCollector {
	return &recordingSubCollector{
		failureCounts:   make(map[ConsumeFailureReason]int),
		dlxCounts:       make(map[ConsumeFailureReason]int),
		dlxFailedCounts: make(map[ConsumeFailureReason]int),
	}
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

func (c *recordingSubCollector) RecordDeadLetter(_ context.Context, reason ConsumeFailureReason) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deadLetterCnt++
	c.dlxCounts[reason]++
}

func (c *recordingSubCollector) RecordDeadLetterFailure(_ context.Context, reason ConsumeFailureReason) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.deadLetterFailN++
	c.dlxFailedCounts[reason]++
}

func (c *recordingSubCollector) AdjustInflight(_ context.Context, delta int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.inflightDeltas = append(c.inflightDeltas, delta)
	c.inflightNet += delta
	if c.inflightNet > c.inflightPeak {
		c.inflightPeak = c.inflightNet
	}
}

// inflightSnapshot returns the current net gauge value, the peak observed, and
// the number of AdjustInflight calls.
func (c *recordingSubCollector) inflightSnapshot() (net, peak int64, calls int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.inflightNet, c.inflightPeak, len(c.inflightDeltas)
}

func (c *recordingSubCollector) snapshot() (success, failure int, last ConsumeFailureReason) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.successCount, c.failureCount, c.lastReason
}

// dlxSnapshot returns the total dead-letter count and a per-reason breakdown.
func (c *recordingSubCollector) dlxSnapshot() (total int, byReason map[ConsumeFailureReason]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make(map[ConsumeFailureReason]int, len(c.dlxCounts))
	for k, v := range c.dlxCounts {
		cp[k] = v
	}
	return c.deadLetterCnt, cp
}

// dlxFailedSnapshot returns the total dead-letter-publish-failure count and a
// per-reason breakdown.
func (c *recordingSubCollector) dlxFailedSnapshot() (total int, byReason map[ConsumeFailureReason]int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make(map[ConsumeFailureReason]int, len(c.dlxFailedCounts))
	for k, v := range c.dlxFailedCounts {
		cp[k] = v
	}
	return c.deadLetterFailN, cp
}
