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
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/command"
)

// activeUniquenessTestDeadline is a fixed future deadline shared across
// active-uniqueness relay tests (no inline literals per TEST-TIME-LITERAL-01).
var activeUniquenessTestDeadline = time.Date(2030, 6, 1, 0, 0, 0, 0, time.UTC)

// fakeClaimer is a programmable Claimer for asserting each ClaimState branch.
// lastDoneTTL records the done-key TTL the relay passed to the most recent Claim,
// so tests can assert active-uniqueness done-TTL truncation (#1820 F1).
type fakeClaimer struct {
	state       idempotency.ClaimState
	claimErr    error
	receipt     *spyReceipt
	claims      int
	lastDoneTTL time.Duration
}

func (c *fakeClaimer) Claim(_ context.Context, _ string, _, doneTTL time.Duration) (idempotency.ClaimState, idempotency.Receipt, error) {
	c.claims++
	c.lastDoneTTL = doneTTL
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
	// dispatchCommand reads r.clk().Now() to bound an active-uniqueness done-TTL;
	// the active-uniqueness truncation tests override this with a clockmock.
	r.clock = clock.Real()
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
		claimer,
	)

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
		claimer,
	)

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
		claimer,
	)

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
		claimer,
	)

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
		claimer,
	)

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
	mc := &kindMetricsCollector{}
	cfg := RelayConfig{}.WithDefaults()
	cfg.Metrics = mc
	r := NewRelay(clock.Real(), store, &recordingPublisher{}, cfg)
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

	// A deduped command (no dispatch, nil receipt) is still a command: it must land
	// in the command kind bucket like a first dispatch, never the event bucket — the
	// kind attribution is the publishBatch discriminator, not the claim outcome (#1674).
	event, cmd := mc.totals()
	assert.Equal(t, 1, cmd.Published, "deduped command settles to the command kind bucket")
	assert.Zero(t, event, "a deduped command must NOT be attributed to the event bucket")
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
		claimer,
	)

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

// ---------------------------------------------------------------------------
// Active-uniqueness ctx injection tests (#1820)
// ---------------------------------------------------------------------------

// claimedEntryWithDeadline builds a ClaimedEntry that carries both the full
// command idempotency identity AND CommandDeadlineMetadataKey set to the given
// deadline formatted as RFC3339Nano. It is the relay-side counterpart to a
// producer that called EmitAsync with WithActiveUniqueness.
func claimedEntryWithDeadline(t *testing.T, id string, dl time.Time) ClaimedEntry {
	t.Helper()
	now := time.Now()
	e, err := kout.EntryScan{
		// Topic is fixed to testCmdID so the entry routes to the test dispatcher;
		// only id (the entry identity) and dl (the deadline) are parameterized.
		ID: id, AggregateID: "subject-" + id, EventType: testCmdID, Topic: testCmdID,
		Payload: []byte(`{}`),
		Metadata: map[string]string{
			command.CommandIDMetadataKey:       "cmd-" + id,
			command.CommandDeadlineMetadataKey: dl.UTC().Format(time.RFC3339Nano),
		},
		CreatedAt:  now,
		OccurredAt: now,
	}.ToEntry()
	require.NoError(t, err)
	return ClaimedEntry{Entry: e, LeaseID: "lease-1"}
}

// claimedEntryWithBadDeadline builds a ClaimedEntry with a corrupt (unparseable)
// CommandDeadlineMetadataKey value to exercise the fail-closed dead-letter path.
func claimedEntryWithBadDeadline(t *testing.T, id, topic string) ClaimedEntry {
	t.Helper()
	now := time.Now()
	e, err := kout.EntryScan{
		ID: id, AggregateID: "subject-" + id, EventType: topic, Topic: topic,
		Payload: []byte(`{}`),
		Metadata: map[string]string{
			command.CommandIDMetadataKey:       "cmd-" + id,
			command.CommandDeadlineMetadataKey: "not-a-valid-rfc3339-timestamp",
		},
		CreatedAt:  now,
		OccurredAt: now,
	}.ToEntry()
	require.NoError(t, err)
	return ClaimedEntry{Entry: e, LeaseID: "lease-1"}
}

// TestDispatchCommand_ActiveUniqueness_InjectsCtx asserts that when an entry
// carries a valid CommandDeadlineMetadataKey, the relay injects
// (claimKey, deadline) into the dispatch ctx via command.WithDispatchedUniqueness,
// and the handler observes them via command.DispatchedUniqueness.
func TestDispatchCommand_ActiveUniqueness_InjectsCtx(t *testing.T) {
	t.Parallel()
	rcpt := &spyReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: rcpt}

	var (
		gotKey      string
		gotDeadline time.Time
		gotOK       bool
	)
	r := relayWithDispatch(
		func(ctx context.Context, _ *command.Registry, _ kout.Entry) error {
			gotKey, gotDeadline, gotOK = command.DispatchedUniqueness(ctx)
			return nil
		},
		claimer,
	)

	results := r.publishBatch(context.Background(),
		[]ClaimedEntry{claimedEntryWithDeadline(t, "c1", activeUniquenessTestDeadline)})
	require.Len(t, results, 1)
	require.NoError(t, results[0].err)
	assert.True(t, gotOK, "DispatchedUniqueness ok must be true when CommandDeadlineMetadataKey is present")
	assert.NotEmpty(t, gotKey, "DispatchedUniqueness key must be non-empty")
	assert.True(t, activeUniquenessTestDeadline.Equal(gotDeadline),
		"DispatchedUniqueness deadline mismatch: got %v, want %v", gotDeadline, activeUniquenessTestDeadline)
}

// TestDispatchCommand_NoDeadlineMetadata_NoCtxInjection asserts that when an
// entry does NOT carry CommandDeadlineMetadataKey, the relay dispatches the
// handler without injecting anything into ctx (DispatchedUniqueness returns
// ok=false), preserving exact backward-compatible behavior.
func TestDispatchCommand_NoDeadlineMetadata_NoCtxInjection(t *testing.T) {
	t.Parallel()
	rcpt := &spyReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: rcpt}

	var gotOK bool
	r := relayWithDispatch(
		func(ctx context.Context, _ *command.Registry, _ kout.Entry) error {
			_, _, gotOK = command.DispatchedUniqueness(ctx)
			return nil
		},
		claimer,
	)

	// claimedEntry (from relay_command_test.go) does not set CommandDeadlineMetadataKey.
	results := r.publishBatch(context.Background(),
		[]ClaimedEntry{claimedEntry(t, "c1", testCmdID, `{}`)})
	require.Len(t, results, 1)
	require.NoError(t, results[0].err)
	assert.False(t, gotOK, "DispatchedUniqueness ok must be false when CommandDeadlineMetadataKey is absent")
}

// deadlineOnlyEntry builds a ClaimedEntry that carries CommandDeadlineMetadataKey
// but NO CommandIDMetadataKey (and no AggregateID), so ClaimKeyFromEntry returns
// ok=false. This is the "deadline present but identity missing" boundary case that
// must fire the missing-identity guard BEFORE the deadline injection path.
func deadlineOnlyEntry(t *testing.T, id, topic string, dl time.Time) ClaimedEntry {
	t.Helper()
	now := time.Now()
	e, err := kout.EntryScan{
		ID: id, EventType: topic, Topic: topic,
		Payload: []byte(`{}`),
		Metadata: map[string]string{
			// Deadline present, but no CommandIDMetadataKey and no AggregateID.
			command.CommandDeadlineMetadataKey: dl.UTC().Format(time.RFC3339Nano),
		},
		CreatedAt:  now,
		OccurredAt: now,
	}.ToEntry()
	require.NoError(t, err)
	return ClaimedEntry{Entry: e, LeaseID: "lease-1"}
}

// TestDispatchCommand_DeadlinePresentButIdentityMissing_Permanent asserts that
// when an entry carries CommandDeadlineMetadataKey but is missing the command
// idempotency identity (no AggregateID, no CommandIDMetadataKey), dispatchCommand
// treats it as a PERMANENT error / dead-letter. The missing-identity guard fires
// BEFORE the deadline injection path — the Claimer is never consulted and the
// handler is never called. This locks the check ordering (identity-first).
func TestDispatchCommand_DeadlinePresentButIdentityMissing_Permanent(t *testing.T) {
	t.Parallel()
	rcpt := &spyReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: rcpt}

	var dispatched int
	r := relayWithDispatch(
		func(context.Context, *command.Registry, kout.Entry) error { dispatched++; return nil },
		claimer,
	)

	results := r.publishBatch(context.Background(),
		[]ClaimedEntry{deadlineOnlyEntry(t, "c1", testCmdID, activeUniquenessTestDeadline)})
	require.Len(t, results, 1)
	require.Error(t, results[0].err)
	assert.True(t, isPermanentDispatch(results[0].err),
		"deadline-present-but-identity-missing must be permanent (→ MarkDead): %v", results[0].err)
	assert.Equal(t, 0, dispatched, "missing identity must NOT dispatch the handler")
	assert.Equal(t, 0, claimer.claims, "missing identity must fail-closed before Claim (identity-first ordering)")
}

// TestDispatchCommand_UnparseableDeadline_DeadLetters asserts that a corrupt
// CommandDeadlineMetadataKey value causes fail-closed dead-letter (permanent
// error) and the handler is never called.
//
// F4 (#1820): the deadline is parsed BEFORE Claim, so a corrupt value dead-letters
// WITHOUT acquiring a claim — the Claimer is never consulted (claims==0) and no
// receipt is held, so there is no live command claim to leak. (Before the fix the
// parse happened after ClaimAcquired and returned a result with no receipt, so
// handleFailedEntry could not Release the acquired claim — a settlement leak.)
func TestDispatchCommand_UnparseableDeadline_DeadLetters(t *testing.T) {
	t.Parallel()
	rcpt := &spyReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: rcpt}

	var dispatched int
	r := relayWithDispatch(
		func(context.Context, *command.Registry, kout.Entry) error { dispatched++; return nil },
		claimer,
	)

	results := r.publishBatch(context.Background(),
		[]ClaimedEntry{claimedEntryWithBadDeadline(t, "c1", testCmdID)})
	require.Len(t, results, 1)
	require.Error(t, results[0].err)
	assert.True(t, isPermanentDispatch(results[0].err),
		"unparseable deadline must be permanent (→ MarkDead): %v", results[0].err)
	assert.Equal(t, 0, dispatched, "unparseable deadline must NOT dispatch the handler")
	assert.Equal(t, 0, claimer.claims,
		"corrupt deadline must fail-closed BEFORE Claim (no claim acquired → no receipt to leak)")
	assert.Equal(t, 0, rcpt.released,
		"no receipt is acquired on the pre-Claim corrupt-deadline path, so none is released")
}

// activeUniquenessTruncBase / activeUniquenessTruncWindow are the controllable
// clock base and the active deadline offset for the done-TTL truncation tests
// (TEST-TIME-LITERAL-01: named, not inline literals).
var activeUniquenessTruncBase = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)

const (
	activeUniquenessTruncWindow = 36 * time.Hour // active deadline offset (AttemptTTL)
	activeUniquenessTruncTick   = 12 * time.Hour // reconcile sweep-interval tick
)

// TestDispatchCommand_ActiveUniqueness_DoneTTLTruncatedToDeadline asserts the
// relay bounds an active-uniqueness command's Claimer done-key TTL to its terminal
// deadline (#1820 F1) rather than the 24h default — so the done-key expires exactly
// when the command it created does and cannot suppress the terminal-release retry.
func TestDispatchCommand_ActiveUniqueness_DoneTTLTruncatedToDeadline(t *testing.T) {
	t.Parallel()
	rcpt := &spyReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: rcpt}
	r := relayWithDispatch(
		func(context.Context, *command.Registry, kout.Entry) error { return nil }, claimer)
	r.clock = clockmock.New(activeUniquenessTruncBase)

	deadline := activeUniquenessTruncBase.Add(activeUniquenessTruncWindow)
	results := r.publishBatch(context.Background(),
		[]ClaimedEntry{claimedEntryWithDeadline(t, "trunc-future", deadline)})
	require.Len(t, results, 1)
	require.NoError(t, results[0].err)
	assert.Equal(t, activeUniquenessTruncWindow, claimer.lastDoneTTL,
		"active-uniqueness done-TTL must be truncated to (deadline - now), not the 24h default")
	assert.NotEqual(t, commandDoneTTL, claimer.lastDoneTTL,
		"truncated done-TTL must differ from the non-active 24h default")
}

// TestDispatchCommand_ActiveUniqueness_PastDeadlineClampsDoneTTLToZero asserts a
// deadline already in the past clamps the done-TTL to 0 (the command is/was
// terminal) so the done-key expires immediately and the next dispatch re-acquires.
func TestDispatchCommand_ActiveUniqueness_PastDeadlineClampsDoneTTLToZero(t *testing.T) {
	t.Parallel()
	rcpt := &spyReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: rcpt}
	r := relayWithDispatch(
		func(context.Context, *command.Registry, kout.Entry) error { return nil }, claimer)
	r.clock = clockmock.New(activeUniquenessTruncBase)

	pastDeadline := activeUniquenessTruncBase.Add(-activeUniquenessTruncWindow)
	results := r.publishBatch(context.Background(),
		[]ClaimedEntry{claimedEntryWithDeadline(t, "trunc-past", pastDeadline)})
	require.Len(t, results, 1)
	require.NoError(t, results[0].err)
	assert.Equal(t, time.Duration(0), claimer.lastDoneTTL,
		"a past deadline must clamp the done-TTL to 0 (immediate done-key expiry → re-dispatch)")
}

// TestDispatchCommand_NonActive_UsesDefaultDoneTTL asserts a command WITHOUT a
// deadline (no active-uniqueness opt-in) keeps the framework 24h source-redelivery
// dedup window — the truncation applies only to active-uniqueness commands.
func TestDispatchCommand_NonActive_UsesDefaultDoneTTL(t *testing.T) {
	t.Parallel()
	rcpt := &spyReceipt{}
	claimer := &fakeClaimer{state: idempotency.ClaimAcquired, receipt: rcpt}
	r := relayWithDispatch(
		func(context.Context, *command.Registry, kout.Entry) error { return nil }, claimer)

	// claimedEntry carries identity but no CommandDeadlineMetadataKey.
	results := r.publishBatch(context.Background(),
		[]ClaimedEntry{claimedEntry(t, "c1", testCmdID, `{}`)})
	require.Len(t, results, 1)
	require.NoError(t, results[0].err)
	assert.Equal(t, commandDoneTTL, claimer.lastDoneTTL,
		"non-active command must keep the 24h source-redelivery dedup window")
}

// TestDispatchCommand_ActiveUniqueness_RealClaimer_TerminalReleaseRetry is the
// end-to-end timing proof for #1820 F1, driving the REAL idempotency.InMemClaimer
// (not the fakeClaimer) under a controllable clock. It mirrors the cert-renewal
// loop: every tick re-derives the SAME Claimer key (stable subject+commandID, like
// rotateCommandID(device, epoch)) and the producer's first emit fixes the queue
// command's terminal deadline at T0+AttemptTTL.
//
// It asserts the property the reviewer flagged as untested: while the command is
// active the relay coalesces re-emits to ClaimDone (no duplicate dispatch), and
// AFTER the deadline — when the queue would release the active-uniqueness key — the
// relay's done-key has ALSO expired (because it was truncated to the deadline), so
// the next tick re-dispatches. Before the truncation fix the fixed 24h done-key got
// refreshed past the deadline by the coalesced re-emits and suppressed this retry.
// Each tick settles the live receipt's Commit directly here (mirroring writeBackOne
// on success), since publishBatch does not run write-back.
func TestDispatchCommand_ActiveUniqueness_RealClaimer_TerminalReleaseRetry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fc := clockmock.New(activeUniquenessTruncBase)
	claimer := idempotency.NewInMemClaimer(fc)

	var dispatched int
	r := relayWithDispatch(
		func(context.Context, *command.Registry, kout.Entry) error { dispatched++; return nil }, claimer)
	r.clock = fc

	// The deadline of the command created by the FIRST (T0) emit. Later coalesced
	// emits do not change the queue command's deadline, so the done-key the relay
	// commits at T0 is the one that must expire exactly here.
	deadline := activeUniquenessTruncBase.Add(activeUniquenessTruncWindow) // T0 + 36h

	// tick dispatches one re-emit (same key) and Commits the live receipt if the
	// relay acquired the claim (mirrors writeBackOne committing on success).
	tick := func() {
		res := r.publishBatch(ctx,
			[]ClaimedEntry{claimedEntryWithDeadline(t, "c1", deadline)})
		require.Len(t, res, 1)
		require.NoError(t, res[0].err)
		if res[0].receipt != nil {
			require.NoError(t, res[0].receipt.Commit(ctx))
		}
	}

	// T0: first emit acquires and dispatches; done-key committed with TTL truncated
	// to (deadline - T0) = 36h, so it expires at the deadline.
	tick()
	require.Equal(t, 1, dispatched, "T0 emit must dispatch")

	// T+12h and T+24h: still before the deadline → ClaimDone → coalesced, NO dispatch.
	fc.Advance(activeUniquenessTruncTick)
	tick()
	fc.Advance(activeUniquenessTruncTick)
	tick()
	require.Equal(t, 1, dispatched, "emits before the deadline must coalesce (no duplicate dispatch)")

	// Advance PAST the deadline: the queue would release the active-uniqueness key,
	// and the relay done-key (truncated to the deadline) has also expired.
	fc.Advance(activeUniquenessTruncWindow) // now = T0 + 24h + 36h = T0 + 60h > deadline (T0 + 36h)
	tick()
	assert.Equal(t, 2, dispatched,
		"after the deadline the truncated done-key has expired → next tick re-dispatches (terminal-release retry)")
}
