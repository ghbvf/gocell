// Package outbox — white-box tests for F3 (#1673): permanent-vs-transient
// settle classification in handleFailedEntry. A kout.PermanentError-tagged
// failure (deterministic command-dispatch framework error or MarshalEnvelope
// failure) must dead-letter immediately, without consuming the retry budget,
// while an unwrapped (transient) error below MaxAttempts must still retry.
package outbox

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	kout "github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
)

// dispositionCountStore counts MarkDead / MarkRetry and reports the lease CAS as
// won (updated=true), so the disposition handleFailedEntry chooses (dead vs
// retry) is directly observable in stats. Mirrors staleLeaseStore but on the
// success path (lease not lost), isolating the dead-vs-retry decision.
type dispositionCountStore struct {
	*minimalStore
	markDeadCalls  int
	markRetryCalls int
}

func (s *dispositionCountStore) MarkDead(_ context.Context, _, _ string, _ int, _ string) (bool, error) {
	s.markDeadCalls++
	return true, nil
}

func (s *dispositionCountStore) MarkRetry(_ context.Context, _, _ string, _ int, _ time.Time, _ string) (bool, error) {
	s.markRetryCalls++
	return true, nil
}

func newPermanentTestRelay(store Store) *Relay {
	return &Relay{
		store:   store,
		cfg:     RelayConfig{MaxAttempts: 5, BaseRetryDelay: testtime.D1ms, MaxRetryDelay: testtime.D5s}.WithDefaults(),
		metrics: &safeRelayCollector{inner: kout.NoopRelayCollector{}},
		clock:   clock.Real(),
	}
}

func permanentTestResult(id string, attempts int, err error) publishResult {
	entry, scanErr := kout.EntryScan{
		ID: id, EventType: "command.devicecommand.enqueue.v1",
		Topic: "command.devicecommand.enqueue.v1", Payload: []byte(`{}`),
		CreatedAt: time.Now(), OccurredAt: time.Now(),
	}.ToEntry()
	if scanErr != nil {
		panic("permanentTestResult: " + scanErr.Error())
	}
	return publishResult{
		entry: ClaimedEntry{Entry: entry, Attempts: attempts, LeaseID: uuid.NewString()},
		err:   err,
		// command.* topic → command-dispatch branch; mirror publishBatch's
		// isCommand marker so handleFailedEntry attributes the failure to the
		// command bucket (outbox_relayed_total{kind="command"}, #1674).
		isCommand: true,
	}
}

// TestRelay_HandleFailedEntry_PermanentError_MarkDeadWithoutRetry asserts the F3
// core: a kout.PermanentError-tagged failure dead-letters on the FIRST failure
// (newAttempts=1, far below MaxAttempts=5) instead of retrying to budget
// exhaustion. This is exactly what generated DispatchAsync now returns for
// deterministic framework errors (decode / topic-mismatch / no-handler /
// wrong-type) and what publishBatch wraps MarshalEnvelope failures into.
func TestRelay_HandleFailedEntry_PermanentError_MarkDeadWithoutRetry(t *testing.T) {
	store := &dispositionCountStore{minimalStore: newMinimalStore()}
	relay := newPermanentTestRelay(store)

	res := permanentTestResult("e-perm", 0,
		kout.NewPermanentError(fmt.Errorf("dispatch async: no handler registered")))
	var stats pollStats

	err := relay.handleFailedEntry(context.Background(), res, &stats)
	require.NoError(t, err)
	assert.Equal(t, 1, store.markDeadCalls,
		"permanent error must MarkDead on first failure (no retry budget burn)")
	assert.Zero(t, store.markRetryCalls, "permanent error must NOT MarkRetry")
	assert.Equal(t, 1, stats.command.Dead)
	assert.Zero(t, stats.command.Retried)
	assert.Zero(t, stats.event, "command failure must not touch the event bucket")
}

// TestRelay_HandleFailedEntry_PermanentError_WrappedDeep verifies the
// classification survives an extra fmt.Errorf %w wrap around the PermanentError
// (errors.As walks the chain), so a handler that wraps a returned permanent error
// is still dead-lettered.
func TestRelay_HandleFailedEntry_PermanentError_WrappedDeep(t *testing.T) {
	store := &dispositionCountStore{minimalStore: newMinimalStore()}
	relay := newPermanentTestRelay(store)

	deep := fmt.Errorf("relay context: %w",
		kout.NewPermanentError(fmt.Errorf("entry routing topic does not match DispatchID")))
	res := permanentTestResult("e-perm-deep", 0, deep)
	var stats pollStats

	require.NoError(t, relay.handleFailedEntry(context.Background(), res, &stats))
	assert.Equal(t, 1, store.markDeadCalls, "wrapped permanent error must still MarkDead")
	assert.Zero(t, store.markRetryCalls)
}

// TestRelay_HandleFailedEntry_TransientError_MarkRetry is the control: an
// unwrapped (transient) error below MaxAttempts must retry, proving the permanent
// branch does not over-trigger on ordinary handler / broker business errors.
func TestRelay_HandleFailedEntry_TransientError_MarkRetry(t *testing.T) {
	store := &dispositionCountStore{minimalStore: newMinimalStore()}
	relay := newPermanentTestRelay(store)

	res := permanentTestResult("e-transient", 0, fmt.Errorf("transient handler error"))
	var stats pollStats

	require.NoError(t, relay.handleFailedEntry(context.Background(), res, &stats))
	assert.Equal(t, 1, store.markRetryCalls,
		"transient error below MaxAttempts must MarkRetry")
	assert.Zero(t, store.markDeadCalls, "transient error must NOT MarkDead")
	assert.Equal(t, 1, stats.command.Retried)
	assert.Zero(t, stats.command.Dead)
}
