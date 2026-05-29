package mqtt

import (
	"context"
	"sync"
	"time"

	"github.com/eclipse/paho.golang/paho"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// subAckRecorder is a minimal mqttAcker test double used by the dispatchAck
// white-box tests. It is defined here (in the no-build-tag shared helper file)
// rather than reusing connection_receive_test.go's fakeAcker because that type
// is gated behind //go:build !integration and so is invisible to the
// integration test binary that also compiles this shared file.
type subAckRecorder struct {
	err error
}

func (a *subAckRecorder) Ack(*paho.Publish) error { return a.err }

// fakeAckConn builds a *Connection that is NOT wired to any broker: it only
// carries a (possibly failing) ackClient so white-box tests can drive
// dispatchAck's ack path deterministically. A Subscriber built around the real
// in-process broker cannot fail the ack — onPublishReceived overwrites
// c.ackClient with the live delivering client on every PUBLISH — so the only
// way to exercise the "ack fails after commit succeeds" branch at unit level is
// to call dispatchAck directly against a Connection whose ackClient is a
// failing fake. ackErr nil → ack succeeds.
func fakeAckConn(ackErr error) *Connection {
	return &Connection{ackClient: &subAckRecorder{err: ackErr}}
}

// newDispatchAckSubscriber constructs a Subscriber bound to a fake-ack
// Connection (see fakeAckConn) with the given collector, for white-box
// dispatchAck branch tests. The namespace is the canonical "test" namespace so
// NewSubscriber's non-zero-namespace guard passes; no broker SUBSCRIBE happens.
func newDispatchAckSubscriber(t testingT, ackErr error, collector SubscriberCollector) *Subscriber {
	ns, err := ParseTopicNamespace("test")
	if err != nil {
		t.Fatalf("ParseTopicNamespace: %v", err)
	}
	sub, err := NewSubscriber(clock.Real(), fakeAckConn(ackErr), ns, SubscriberConfig{},
		WithSubscriberCollector(collector))
	if err != nil {
		t.Fatalf("NewSubscriber: %v", err)
	}
	return sub
}

// testingT is the minimal testing surface newDispatchAckSubscriber needs. It is
// satisfied by *testing.T in both the unit and integration build.
type testingT interface {
	Fatalf(format string, args ...any)
}

// dispatchAckEntry returns a benign entry + publish pair for dispatchAck tests.
func dispatchAckEntry(topic string) (*paho.Publish, outbox.Entry) {
	return &paho.Publish{Topic: topic, QoS: 1},
		outbox.Entry{ID: "evt-dispatch-ack", EventType: "test.event", Topic: topic, Payload: []byte(`{}`)}
}

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
