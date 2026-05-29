//go:build !integration

package mqtt

import (
	"context"

	"github.com/eclipse/paho.golang/paho"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// This file is unit-only (//go:build !integration). The dispatchAck white-box
// test doubles below have no caller in the integration build (subscriber_test.go
// is !integration), so keeping them in the no-tag shared helper file made them
// report `unused` under -tags=integration. They live here, gated to the unit
// build, alongside their sole consumer subscriber_test.go.

// subAckRecorder is a minimal mqttAcker test double used by the dispatchAck
// white-box tests. It is defined here rather than reusing
// connection_receive_test.go's fakeAcker because that type is also
// //go:build !integration but is colocated with broker-backed tests; this keeps
// the dispatchAck doubles self-contained.
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

// testingT is the minimal testing surface newDispatchAckSubscriber needs.
type testingT interface {
	Fatalf(format string, args ...any)
}

// dispatchAckEntry returns a benign entry + publish pair for dispatchAck tests.
func dispatchAckEntry(topic string) (*paho.Publish, outbox.Entry) {
	return &paho.Publish{Topic: topic, QoS: 1},
		outbox.Entry{ID: "evt-dispatch-ack", EventType: "test.event", Topic: topic, Payload: []byte(`{}`)}
}

// ackHandler is a trivial SubscriberHandler that always Acks with no settlement.
// Used by tests that exercise Subscribe/Ready lifecycle and do not assert on the
// handler result itself.
func ackHandler(_ context.Context, _ outbox.Entry) (outbox.HandleResult, outbox.Settlement) {
	return outbox.Ack(), nil
}
