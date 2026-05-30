//go:build !integration

package mqtt

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
)

// deadLetterSubscriber builds a Subscriber around the given Connection with a
// recording collector, for white-box routeDeadLetter branch tests. The namespace
// is the canonical "test" namespace so NewSubscriber's non-zero guard passes.
func deadLetterSubscriber(t *testing.T, conn *Connection) (*Subscriber, *recordingSubCollector) {
	t.Helper()
	ns, err := ParseTopicNamespace("test")
	require.NoError(t, err)
	coll := newRecordingSubCollector()
	sub, err := NewSubscriber(clock.Real(), conn, ns, SubscriberConfig{}, WithSubscriberCollector(coll))
	require.NoError(t, err)
	return sub, coll
}

// TestRouteDeadLetter_MintFailure_SkipsPublish verifies that when the original
// topic cannot be minted into a $dead target (out-of-namespace poison topic),
// routeDeadLetter logs and returns WITHOUT attempting a publish and WITHOUT
// recording a dead-letter (fail-closed: no $dead capture for an unmintable topic).
// Uses fakeAckConn (nil ConnectionManager) — proving the publish path is never
// reached, since touching conn.Publish would nil-panic.
func TestRouteDeadLetter_MintFailure_SkipsPublish(t *testing.T) {
	t.Parallel()
	sub, coll := deadLetterSubscriber(t, fakeAckConn(nil))

	// "other/x" is outside the "test" namespace → MintDeadLetter fails before any
	// publish. If routeDeadLetter reached conn.Publish, the nil cm would panic.
	sub.routeDeadLetter(context.Background(), "other/x", []byte(`{"k":"v"}`), consumeReasonReject)

	total, _ := coll.dlxSnapshot()
	assert.Equal(t, 0, total, "no dead-letter recorded when the topic cannot be minted")
}

// TestRouteDeadLetter_PublishFailure_NoRecord verifies that when the $dead
// publish itself fails (here: a closed Connection), routeDeadLetter logs the
// failure and returns WITHOUT recording a dead-letter — the message will be
// acked-as-poison without DLT capture (fail-closed: never block intake on a
// dead-letter publish failure).
func TestRouteDeadLetter_PublishFailure_NoRecord(t *testing.T) {
	t.Parallel()
	// A closed Connection makes Publish return ErrAdapterMQTTClosed before
	// touching the (nil) ConnectionManager.
	sub, coll := deadLetterSubscriber(t, &Connection{closed: true})

	sub.routeDeadLetter(context.Background(), "test/foo", []byte(`{"k":"v"}`), consumeReasonReject)

	total, _ := coll.dlxSnapshot()
	assert.Equal(t, 0, total, "no dead-letter recorded when the $dead publish fails")
}

// TestRouteDeadLetter_Success_RecordsDLX verifies the happy path against the
// in-process broker: routeDeadLetter mints "$dead/test/foo", publishes the
// original payload, and records the dead-letter with the given reason.
func TestRouteDeadLetter_Success_RecordsDLX(t *testing.T) {
	t.Parallel()
	addr, stop := startInternalBroker(t)
	defer stop()
	sub, conn := newTestSubscriber(t, addr, nil)
	_ = conn
	coll := newRecordingSubCollector()
	// Rebind the collector so we can assert on dlx counts (newTestSubscriber's
	// collector arg was nil to keep the helper signature simple).
	sub.collector = coll

	sub.routeDeadLetter(context.Background(), "test/foo", []byte(`{"k":"v"}`), consumeReasonReject)

	total, byReason := coll.dlxSnapshot()
	assert.Equal(t, 1, total, "dead-letter recorded on successful $dead publish")
	assert.Equal(t, 1, byReason[consumeReasonReject], "dlx recorded under the reject reason")
}
