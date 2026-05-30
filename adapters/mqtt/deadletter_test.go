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
	assert.Equal(t, 0, total, "no dead-letter capture recorded when the topic cannot be minted")
	failTotal, failByReason := coll.dlxFailedSnapshot()
	assert.Equal(t, 1, failTotal, "mint failure must record a dead-letter failure (alertable)")
	assert.Equal(t, 1, failByReason[consumeReasonReject], "failure recorded under the reject reason")
}

// TestRouteDeadLetter_PublishFailure_RecordsFailure verifies that when the $dead
// publish itself fails (here: a closed Connection), routeDeadLetter logs the
// failure, records the alertable dead-letter-failure metric, and returns WITHOUT
// recording a successful capture — the message is acked-as-poison without DLT
// capture (fail-closed: a dead-letter publish failure never blocks intake, but it
// is now observable via mqtt_dlx_failed_total — F-1/F-2, ref Kafka Connect KIP-298).
func TestRouteDeadLetter_PublishFailure_RecordsFailure(t *testing.T) {
	t.Parallel()
	// A closed Connection makes Publish return ErrAdapterMQTTClosed before
	// touching the (nil) ConnectionManager.
	sub, coll := deadLetterSubscriber(t, &Connection{closed: true})

	sub.routeDeadLetter(context.Background(), "test/foo", []byte(`{"k":"v"}`), consumeReasonReject)

	total, _ := coll.dlxSnapshot()
	assert.Equal(t, 0, total, "no dead-letter capture recorded when the $dead publish fails")
	failTotal, failByReason := coll.dlxFailedSnapshot()
	assert.Equal(t, 1, failTotal, "publish failure must record a dead-letter failure (alertable)")
	assert.Equal(t, 1, failByReason[consumeReasonReject], "failure recorded under the reject reason")
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
