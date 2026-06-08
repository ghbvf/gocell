// Package outbox — white-box tests for the relay command-dispatch Claimer wrap
// (#1698). They live in package outbox to reach the unexported dispatchCommand /
// publishBatch / cmdClaimer and bind FAKE AsyncDispatchFunc + Claimer values.
package outbox

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/command"
)

// fakeClaimer is a programmable Claimer for asserting each ClaimState branch.
type fakeClaimer struct {
	state    idempotency.ClaimState
	claimErr error
	receipt  *spyReceipt
	claims   int
}

func (c *fakeClaimer) Claim(_ context.Context, _ string, _, _ time.Duration) (idempotency.ClaimState, idempotency.Receipt, error) {
	c.claims++
	if c.claimErr != nil {
		return idempotency.ClaimBusy, nil, c.claimErr
	}
	if c.state == idempotency.ClaimAcquired {
		return idempotency.ClaimAcquired, c.receipt, nil
	}
	return c.state, idempotency.NonAcquiredReceipt(), nil
}

func (c *fakeClaimer) Kind() idempotency.ClaimerKind { return idempotency.ClaimerKindInMemory }

// spyReceipt records Commit / Release so tests can assert lease settlement. The
// commitErr / releaseErr fields let a test inject a settle-side store failure.
// commitCtxErr / releaseCtxErr record whether the settle context was already
// canceled when the operation was invoked.
type spyReceipt struct {
	committed     int
	released      int
	commitErr     error
	releaseErr    error
	commitCtxErr  error
	releaseCtxErr error
}

func (r *spyReceipt) Commit(ctx context.Context) error {
	r.committed++
	r.commitCtxErr = ctx.Err()
	return r.commitErr
}
func (r *spyReceipt) Release(ctx context.Context) error {
	r.released++
	r.releaseCtxErr = ctx.Err()
	return r.releaseErr
}
func (r *spyReceipt) Extend(context.Context, time.Duration) error { return nil }

// noIdentityEntry builds a command ClaimedEntry with NO idempotency identity
// (no AggregateID, no commandID metadata) so ClaimKeyFromEntry returns ok=false.
func noIdentityEntry(t *testing.T, id, topic string) ClaimedEntry {
	t.Helper()
	now := time.Now()
	e, err := kout.EntryScan{
		ID: id, EventType: topic, Topic: topic, Payload: []byte(`{}`),
		CreatedAt: now, OccurredAt: now,
	}.ToEntry()
	require.NoError(t, err)
	return ClaimedEntry{Entry: e, LeaseID: "lease-1"}
}

// testCmdID is the single command topic every dispatch test seeds and routes;
// relayWithDispatch builds its dispatcher map against it (callers seed entries
// with the same value via their local cmdID const).
const testCmdID = "command.test.do.v1"

// relayLifecycleTimeout bounds the Start/Ready/stop waits in the nil-guard
// lifecycle tests (TEST-TIME-LITERAL-01: no inline test-time duration literals).
const relayLifecycleTimeout = 2 * time.Second

func relayWithDispatch(fn command.AsyncDispatchFunc, claimer idempotency.Claimer) *Relay {
	r := &Relay{}
	r.pub = &recordingPublisher{}
	r.WithCommandDispatch(command.NewRegistry(),
		map[command.CommandID]command.AsyncDispatchFunc{command.CommandID(testCmdID): fn}, claimer)
	return r
}

// TestDispatchCommand_ClaimAcquired_DispatchesAndCarriesReceipt asserts a fresh
// claim dispatches the handler and the live receipt is carried in the result.
func TestDispatchCommand_ClaimAcquired_DispatchesAndCarriesReceipt(t *testing.T) {
	t.Parallel()
	const cmdID = "command.test.do.v1"
	rcpt := &spyReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: rcpt}

	var dispatched int
	r := relayWithDispatch(
		func(context.Context, *command.Registry, kout.Entry) error { dispatched++; return nil },
		claimer)

	results := r.publishBatch(context.Background(), []ClaimedEntry{claimedEntry(t, "c1", cmdID, `{}`)})
	require.Len(t, results, 1)
	assert.NoError(t, results[0].err)
	assert.Equal(t, 1, dispatched, "ClaimAcquired must dispatch the handler")
	assert.Same(t, idempotency.Receipt(rcpt), results[0].receipt, "live receipt must be carried for ClaimAcquired")
	assert.Equal(t, 1, claimer.claims)
}

// TestDispatchCommand_ClaimDone_DedupsWithoutDispatch asserts an already-processed
// command is settled as success WITHOUT dispatching and WITHOUT a live receipt.
func TestDispatchCommand_ClaimDone_DedupsWithoutDispatch(t *testing.T) {
	t.Parallel()
	const cmdID = "command.test.do.v1"
	claimer := &fakeClaimer{state: idempotency.ClaimDone}

	var dispatched int
	r := relayWithDispatch(
		func(context.Context, *command.Registry, kout.Entry) error { dispatched++; return nil },
		claimer)

	results := r.publishBatch(context.Background(), []ClaimedEntry{claimedEntry(t, "c1", cmdID, `{}`)})
	require.Len(t, results, 1)
	assert.NoError(t, results[0].err, "ClaimDone settles as success (deduped)")
	assert.Equal(t, 0, dispatched, "ClaimDone must NOT dispatch (deduped)")
	assert.Nil(t, results[0].receipt, "ClaimDone carries no live receipt")
}

// TestDispatchCommand_ClaimBusy_Transient asserts a busy claim yields a transient
// (non-permanent) error so writeBack routes it to MarkRetry.
func TestDispatchCommand_ClaimBusy_Transient(t *testing.T) {
	t.Parallel()
	const cmdID = "command.test.do.v1"
	claimer := &fakeClaimer{state: idempotency.ClaimBusy}

	r := relayWithDispatch(
		func(context.Context, *command.Registry, kout.Entry) error { return nil },
		claimer)

	results := r.publishBatch(context.Background(), []ClaimedEntry{claimedEntry(t, "c1", cmdID, `{}`)})
	require.Len(t, results, 1)
	require.Error(t, results[0].err)
	assert.False(t, isPermanentDispatch(results[0].err), "ClaimBusy must be transient (→ MarkRetry)")
}

// TestDispatchCommand_ClaimInfraError_Transient asserts a Claim infrastructure
// error is transient (not permanent) so it retries rather than dead-letters.
func TestDispatchCommand_ClaimInfraError_Transient(t *testing.T) {
	t.Parallel()
	const cmdID = "command.test.do.v1"
	claimer := &fakeClaimer{claimErr: errors.New("redis down")}

	r := relayWithDispatch(
		func(context.Context, *command.Registry, kout.Entry) error { return nil },
		claimer)

	results := r.publishBatch(context.Background(), []ClaimedEntry{claimedEntry(t, "c1", cmdID, `{}`)})
	require.Len(t, results, 1)
	require.Error(t, results[0].err)
	assert.False(t, isPermanentDispatch(results[0].err), "Claim infra error must be transient (→ MarkRetry)")
}

// TestDispatchCommand_MissingIdentity_Permanent asserts a command entry without
// idempotency identity is dead-lettered (permanent error), never dispatched, and
// the Claimer is never consulted (fail-closed before Claim).
func TestDispatchCommand_MissingIdentity_Permanent(t *testing.T) {
	t.Parallel()
	const cmdID = "command.test.do.v1"
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: &spyReceipt{}}

	var dispatched int
	r := relayWithDispatch(
		func(context.Context, *command.Registry, kout.Entry) error { dispatched++; return nil },
		claimer)

	results := r.publishBatch(context.Background(), []ClaimedEntry{noIdentityEntry(t, "c1", cmdID)})
	require.Len(t, results, 1)
	require.Error(t, results[0].err)
	assert.True(t, isPermanentDispatch(results[0].err), "missing identity must be permanent (→ MarkDead)")
	assert.Equal(t, 0, dispatched, "missing identity must NOT dispatch")
	assert.Equal(t, 0, claimer.claims, "missing identity must fail-closed before Claim")
}

// TestPollOnce_ClaimDone_DedupSettlesPublished drives the full cycle: a duplicate
// command (ClaimDone) settles the row to published WITHOUT dispatch — the relay
// dedupes a redelivered source event.
func TestPollOnce_ClaimDone_DedupSettlesPublished(t *testing.T) {
	t.Parallel()
	const cmdID = "command.test.do.v1"
	store := newMinimalStore()
	store.seedPendingCommand(cmdID, `{}`)

	var dispatched int
	r := NewRelay(clock.Real(), store, &recordingPublisher{}, RelayConfig{}.WithDefaults())
	r.WithCommandDispatch(command.NewRegistry(),
		map[command.CommandID]command.AsyncDispatchFunc{
			cmdID: func(context.Context, *command.Registry, kout.Entry) error { dispatched++; return nil },
		}, &fakeClaimer{state: idempotency.ClaimDone})

	require.NoError(t, r.pollOnce(context.Background()))
	assert.Equal(t, 0, dispatched, "ClaimDone must not dispatch")
	store.mu.Lock()
	status := store.rows["c1"].status
	store.mu.Unlock()
	assert.Equal(t, "published", status, "deduped command row settles to published")
}

// TestWriteBack_CommitsReceiptOnSuccess asserts the live receipt is Committed
// after a successful dispatch settles to MarkPublished.
func TestWriteBack_CommitsReceiptOnSuccess(t *testing.T) {
	t.Parallel()
	const cmdID = "command.test.do.v1"
	store := newMinimalStore()
	store.seedPendingCommand(cmdID, `{}`)

	rcpt := &spyReceipt{}
	r := NewRelay(clock.Real(), store, &recordingPublisher{}, RelayConfig{}.WithDefaults())
	r.WithCommandDispatch(command.NewRegistry(),
		map[command.CommandID]command.AsyncDispatchFunc{
			cmdID: func(context.Context, *command.Registry, kout.Entry) error { return nil },
		}, &fakeClaimer{state: idempotency.ClaimAcquired, receipt: rcpt})

	require.NoError(t, r.pollOnce(context.Background()))
	assert.Equal(t, 1, rcpt.committed, "successful dispatch must Commit the lease")
	assert.Equal(t, 0, rcpt.released, "successful dispatch must NOT Release the lease")
}

// TestWriteBack_ReleasesReceiptOnFailure asserts the live receipt is Released
// after a failing dispatch settles to MarkRetry.
func TestWriteBack_ReleasesReceiptOnFailure(t *testing.T) {
	t.Parallel()
	const cmdID = "command.test.do.v1"
	store := newMinimalStore()
	store.seedPendingCommand(cmdID, `{}`)

	rcpt := &spyReceipt{}
	r := NewRelay(clock.Real(), store, &recordingPublisher{},
		RelayConfig{MaxAttempts: 5, BaseRetryDelay: time.Millisecond, MaxRetryDelay: time.Second}.WithDefaults())
	r.WithCommandDispatch(command.NewRegistry(),
		map[command.CommandID]command.AsyncDispatchFunc{
			cmdID: func(context.Context, *command.Registry, kout.Entry) error { return errors.New("boom") },
		}, &fakeClaimer{state: idempotency.ClaimAcquired, receipt: rcpt})

	require.NoError(t, r.pollOnce(context.Background()))
	assert.Equal(t, 1, rcpt.released, "failed dispatch must Release the lease")
	assert.Equal(t, 0, rcpt.committed, "failed dispatch must NOT Commit the lease")
	store.mu.Lock()
	status := store.rows["c1"].status
	store.mu.Unlock()
	assert.Equal(t, "pending", status, "failed dispatch retries (back to pending)")
}

// TestDispatchCommand_UnknownState_Permanent asserts a Claimer returning a state
// outside the enumerated set (a state-machine violation) is fail-closed
// dead-lettered (permanent error → MarkDead) and never dispatched.
func TestDispatchCommand_UnknownState_Permanent(t *testing.T) {
	t.Parallel()
	const cmdID = "command.test.do.v1"
	claimer := &fakeClaimer{state: idempotency.ClaimState(99)}

	var dispatched int
	r := relayWithDispatch(
		func(context.Context, *command.Registry, kout.Entry) error { dispatched++; return nil },
		claimer)

	results := r.publishBatch(context.Background(), []ClaimedEntry{claimedEntry(t, "c1", cmdID, `{}`)})
	require.Len(t, results, 1)
	require.Error(t, results[0].err)
	assert.True(t, isPermanentDispatch(results[0].err), "unknown ClaimState must be permanent (→ MarkDead)")
	assert.Equal(t, 0, dispatched, "unknown ClaimState must NOT dispatch")
}

// TestWriteBack_CommitErrorLeavesRowClaiming asserts that idempotency Commit is
// part of the command success boundary. A successful dispatch MUST NOT be marked
// published when the done-key cannot be recorded; leaving the row in claiming lets
// stale-lease recovery retry instead of silently losing dedup protection.
func TestWriteBack_CommitErrorLeavesRowClaiming(t *testing.T) {
	t.Parallel()
	const cmdID = "command.test.do.v1"
	store := newMinimalStore()
	store.seedPendingCommand(cmdID, `{}`)

	rcpt := &spyReceipt{commitErr: errors.New("done-key write failed")}
	r := NewRelay(clock.Real(), store, &recordingPublisher{}, RelayConfig{}.WithDefaults())
	r.WithCommandDispatch(command.NewRegistry(),
		map[command.CommandID]command.AsyncDispatchFunc{
			cmdID: func(context.Context, *command.Registry, kout.Entry) error { return nil },
		}, &fakeClaimer{state: idempotency.ClaimAcquired, receipt: rcpt})

	require.Error(t, r.pollOnce(context.Background()), "Commit failure must surface as writeBack failure")
	assert.Equal(t, 1, rcpt.committed, "Commit is attempted")
	store.mu.Lock()
	status := store.rows["c1"].status
	store.mu.Unlock()
	assert.Equal(t, "claiming", status, "row must not be marked published when Commit fails")
}

// TestWriteBack_ReleaseErrorDoesNotBlockRetry asserts that when the idempotency
// Release fails after a failing dispatch, the row still goes back to "pending"
// (MarkRetry) — the Release failure is logged-not-fatal.
func TestWriteBack_ReleaseErrorDoesNotBlockRetry(t *testing.T) {
	t.Parallel()
	const cmdID = "command.test.do.v1"
	store := newMinimalStore()
	store.seedPendingCommand(cmdID, `{}`)

	rcpt := &spyReceipt{releaseErr: errors.New("lease release failed")}
	r := NewRelay(clock.Real(), store, &recordingPublisher{},
		RelayConfig{MaxAttempts: 5, BaseRetryDelay: time.Millisecond, MaxRetryDelay: time.Second}.WithDefaults())
	r.WithCommandDispatch(command.NewRegistry(),
		map[command.CommandID]command.AsyncDispatchFunc{
			cmdID: func(context.Context, *command.Registry, kout.Entry) error { return errors.New("boom") },
		}, &fakeClaimer{state: idempotency.ClaimAcquired, receipt: rcpt})

	require.NoError(t, r.pollOnce(context.Background()), "Release failure must not surface as a poll error")
	assert.Equal(t, 1, rcpt.released, "Release is attempted")
	store.mu.Lock()
	status := store.rows["c1"].status
	store.mu.Unlock()
	assert.Equal(t, "pending", status, "row goes to pending (retry) despite Release failure")
}

// TestWriteBack_ReleaseUsesDetachedSettleContext asserts Release is still
// attempted with a live context even if the poll caller canceled its ctx before
// writeBack. This prevents cancellation from leaking a busy command claim.
func TestWriteBack_ReleaseUsesDetachedSettleContext(t *testing.T) {
	t.Parallel()
	const cmdID = "command.test.do.v1"
	store := newMinimalStore()
	store.seedPendingCommand(cmdID, `{}`)

	rcpt := &spyReceipt{}
	r := NewRelay(clock.Real(), store, &recordingPublisher{},
		RelayConfig{MaxAttempts: 5, BaseRetryDelay: time.Millisecond, MaxRetryDelay: time.Second}.WithDefaults())
	r.WithCommandDispatch(command.NewRegistry(),
		map[command.CommandID]command.AsyncDispatchFunc{
			cmdID: func(context.Context, *command.Registry, kout.Entry) error { return errors.New("boom") },
		}, &fakeClaimer{state: idempotency.ClaimAcquired, receipt: rcpt})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.NoError(t, r.pollOnce(ctx))
	assert.Equal(t, 1, rcpt.released, "failed dispatch must Release the lease")
	assert.NoError(t, rcpt.releaseCtxErr, "Release must not inherit caller cancellation")
}

// TestStart_CommandDispatchWithoutClaimer_FailsFast asserts wiring command
// dispatch without a Claimer is rejected at Start (fail-closed).
func TestStart_CommandDispatchWithoutClaimer_FailsFast(t *testing.T) {
	t.Parallel()
	const cmdID = "command.test.do.v1"
	r := NewRelay(clock.Real(), newMinimalStore(), &recordingPublisher{}, RelayConfig{}.WithDefaults())
	// Bypass the builder's claimer slot by setting the dispatch map directly so the
	// Start nil-guard is what we exercise (the builder takes a claimer positionally).
	r.cmdDispatch = map[command.CommandID]command.AsyncDispatchFunc{
		command.CommandID(cmdID): func(context.Context, *command.Registry, kout.Entry) error { return nil },
	}

	err := r.Start(context.Background())
	require.Error(t, err, "command dispatch wired without claimer must fail Start")
	assert.Contains(t, err.Error(), "command dispatch wired without claimer")
}

// TestStart_CommandDispatchWithClaimer_StartsAndStops asserts a properly-wired
// command dispatch relay starts and stops cleanly (nil-guard does not false-trip).
func TestStart_CommandDispatchWithClaimer_StartsAndStops(t *testing.T) {
	t.Parallel()
	const cmdID = "command.test.do.v1"
	r := NewRelay(clock.Real(), newMinimalStore(), &recordingPublisher{}, RelayConfig{}.WithDefaults())
	r.WithCommandDispatch(command.NewRegistry(),
		map[command.CommandID]command.AsyncDispatchFunc{
			cmdID: func(context.Context, *command.Registry, kout.Entry) error { return nil },
		}, newCommandClaimer())

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- r.Start(ctx) }()
	select {
	case <-r.Ready():
	case <-time.After(relayLifecycleTimeout):
		t.Fatal("relay did not become ready")
	}
	cancel()
	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(relayLifecycleTimeout):
		t.Fatal("relay did not stop")
	}
}
