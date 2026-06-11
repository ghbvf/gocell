package devicecertrenewal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	cmdenqueue "github.com/ghbvf/gocell/generated/contracts/command/devicecommand/enqueue/v1"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/kernel/reconcile"
	rtcommand "github.com/ghbvf/gocell/runtime/command"
)

var certTestBase = time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)

const certRenewalTestThreshold = 7 * 24 * time.Hour

// certRenewalTestPolicy is the standard test policy. RetryInterval is 24h so
// tests that do not advance the clock (window not elapsed) still see 1 emit.
var certRenewalTestPolicy = Policy{
	Threshold:     certRenewalTestThreshold,
	RetryInterval: 24 * time.Hour,
	BatchSize:     100,
	BatchRequeue:  10 * time.Millisecond,
}

// seedCert persists a device with the given cert state into a mem repo (the
// durable substitute for the old store.Issue seeding).
func seedCert(t *testing.T, ctx context.Context, repo domain.DeviceRepository, id string, expiresAt time.Time) {
	t.Helper()
	require.NoError(t, repo.Create(ctx, &domain.Device{
		ID: id, Name: id, Status: "online", LastSeen: certTestBase,
		CertEpoch: domain.DefaultCertEpoch, CertExpiresAt: expiresAt,
	}))
}

func newTestReconciler(t *testing.T, fc *clockmock.FakeClock, repo domain.DeviceRepository, rec *outboxtest.Recorder) *Reconciler {
	t.Helper()
	r, err := NewReconciler(fc, repo, rec.CellEmitter(), outbox.DemoCellTxManager(),
		certRenewalTestPolicy, nil)
	require.NoError(t, err)
	return r
}

func TestRotateCommandID(t *testing.T) {
	t.Parallel()
	// Stable for the same (deviceID, epoch): the Claimer backstop relies on it.
	assert.Equal(t, rotateCommandID("dev-1", 3), rotateCommandID("dev-1", 3))
	// Epoch-sensitive: a post-rotation re-issue (new epoch) MUST yield a fresh
	// id so the renewal dispatches again rather than deduping forever.
	assert.NotEqual(t, rotateCommandID("dev-1", 1), rotateCommandID("dev-1", 2),
		"different epoch must produce a different command id")
	// Device-sensitive.
	assert.NotEqual(t, rotateCommandID("dev-1", 1), rotateCommandID("dev-2", 1))
}

func TestReconciler_EmitsRenewalForNearExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := mem.NewDeviceRepository()
	// dev-near expires within the threshold; dev-far is well outside it.
	seedCert(t, ctx, repo, "dev-near", certTestBase.Add(24*time.Hour))
	seedCert(t, ctx, repo, "dev-far", certTestBase.Add(30*24*time.Hour))

	rec := outboxtest.NewRecorder()
	r := newTestReconciler(t, clockmock.New(certTestBase), repo, rec)

	_, err := r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)

	entries := rec.Entries()
	require.Len(t, entries, 1, "exactly one renewal command for the single near-expiry device")
	got := entries[0]
	assert.Equal(t, string(cmdenqueue.DispatchID), got.RoutingTopic())
	assert.Equal(t, "dev-near", got.AggregateID(), "subject is the device id")
	assert.Equal(t, rotateCommandID("dev-near", 1), got.Metadata()[rtcommand.CommandIDMetadataKey],
		"command_id is the deterministic per-(device,epoch) dedup token")

	var req cmdenqueue.Request
	require.NoError(t, json.Unmarshal(got.Payload(), &req))
	assert.Equal(t, "dev-near", req.DeviceID)
	assert.Equal(t, rotateCertCommandType, req.CommandType)

	var p rotateCertPayload
	require.NoError(t, json.Unmarshal([]byte(req.Payload), &p))
	assert.Equal(t, int64(1), p.Epoch)
	assert.Equal(t, certTestBase.Add(24*time.Hour).UTC().Format(time.RFC3339), p.NotAfter)
}

func TestReconciler_NoEmitWhenNoneNearExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := mem.NewDeviceRepository()
	seedCert(t, ctx, repo, "dev-far", certTestBase.Add(30*24*time.Hour))

	rec := outboxtest.NewRecorder()
	r := newTestReconciler(t, clockmock.New(certTestBase), repo, rec)

	_, err := r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	assert.Empty(t, rec.Entries(), "no near-expiry cert -> no command emitted")
}

// TestReconciler_DedupsAcrossTicksPerEpoch proves that within the RetryInterval
// window (clock not advanced), the same epoch is NOT re-emitted on the second
// tick. The mark stamps requestedAt=now which is AFTER retryBefore (now -
// RetryInterval), so the cert stays suppressed within the window.
func TestReconciler_DedupsAcrossTicksPerEpoch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := mem.NewDeviceRepository()
	seedCert(t, ctx, repo, "dev-near", certTestBase.Add(24*time.Hour))

	rec := outboxtest.NewRecorder()
	fc := clockmock.New(certTestBase)
	r := newTestReconciler(t, fc, repo, rec)

	_, err := r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)

	// After the first tick the mark must have taken effect. Clock not advanced,
	// so retryBefore = certTestBase - 24h. The mark was stamped at certTestBase
	// (> retryBefore), so the cert is still suppressed within the window.
	retryBefore := certTestBase.Add(-certRenewalTestPolicy.RetryInterval)
	got, err := repo.ListCertificateRenewalCandidates(ctx,
		certTestBase.Add(certRenewalTestThreshold), retryBefore, 100)
	require.NoError(t, err)
	assert.Empty(t, got, "after the first tick marks the epoch, the device is no longer a candidate within the window")

	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)

	assert.Len(t, rec.Entries(), 1, "one emit per epoch within the retry window (repo-level dedup)")
}

// TestReconciler_ReemitsWhenEpochAdvancedPastRequested proves the forcing
// function's != branch: a device whose cert_epoch has advanced past its
// renewal_requested_epoch (a post-rotation re-issue) is re-dispatched with a
// fresh per-epoch command id. The epoch-advance itself (re-issue) is out of this
// reference demo's scope — there is no rotation-completion loop — so the advanced
// state is seeded directly; what is under test is the reconciler re-dispatching
// for any epoch not yet renewal-requested.
func TestReconciler_ReemitsWhenEpochAdvancedPastRequested(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := mem.NewDeviceRepository()
	// epoch 2, but renewal was last requested for epoch 1 -> still a candidate.
	require.NoError(t, repo.Create(ctx, &domain.Device{
		ID: "dev-near", Name: "dev-near", Status: "online", LastSeen: certTestBase,
		CertEpoch: 2, CertExpiresAt: certTestBase.Add(12 * time.Hour), RenewalRequestedEpoch: 1,
	}))

	rec := outboxtest.NewRecorder()
	r := newTestReconciler(t, clockmock.New(certTestBase), repo, rec)

	_, err := r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)

	entries := rec.Entries()
	require.Len(t, entries, 1, "an epoch not yet renewal-requested re-dispatches")
	assert.Equal(t, rotateCommandID("dev-near", 2), entries[0].Metadata()[rtcommand.CommandIDMetadataKey],
		"the command carries the new epoch's id")

	// A second tick now finds renewal_requested_epoch == cert_epoch -> no re-emit within window.
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	assert.Len(t, rec.Entries(), 1, "the advanced epoch is requested at most once within the retry window too")
}

// TestReconciler_RetriesAfterRetryInterval proves that after the RetryInterval
// elapses (clock advanced beyond RetryInterval), the SAME epoch re-enters the
// candidate set and emits the SAME commandID — the level-triggered retry release.
func TestReconciler_RetriesAfterRetryInterval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := mem.NewDeviceRepository()
	seedCert(t, ctx, repo, "dev-near", certTestBase.Add(24*time.Hour))

	rec := outboxtest.NewRecorder()
	fc := clockmock.New(certTestBase)
	r := newTestReconciler(t, fc, repo, rec)

	_, err := r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	require.Len(t, rec.Entries(), 1, "first emit")
	firstCommandID := rec.Entries()[0].Metadata()[rtcommand.CommandIDMetadataKey]

	// Advance the clock beyond RetryInterval — the mark is now stale.
	fc.Advance(certRenewalTestPolicy.RetryInterval + time.Minute)

	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)

	entries := rec.Entries()
	require.Len(t, entries, 2, "after RetryInterval the cert re-enters the candidate set and re-emits")
	assert.Equal(t, firstCommandID, entries[1].Metadata()[rtcommand.CommandIDMetadataKey],
		"re-emit carries the SAME commandID (same epoch) — the relay Claimer dedups within its TTL window")
}

// TestReconciler_NoRetryWithinWindow proves that advancing the clock by LESS than
// RetryInterval keeps the cert suppressed.
func TestReconciler_NoRetryWithinWindow(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := mem.NewDeviceRepository()
	seedCert(t, ctx, repo, "dev-near", certTestBase.Add(24*time.Hour))

	rec := outboxtest.NewRecorder()
	fc := clockmock.New(certTestBase)
	r := newTestReconciler(t, fc, repo, rec)

	_, err := r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	require.Len(t, rec.Entries(), 1, "first emit")

	// Advance by less than RetryInterval — cert is still within the suppression window.
	fc.Advance(certRenewalTestPolicy.RetryInterval - time.Minute)

	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)

	assert.Len(t, rec.Entries(), 1, "cert is still suppressed within the RetryInterval window")
}

// TestReconciler_BatchBoundaryRequeues proves the batch-boundary behavior:
//   - First Reconcile with BatchSize=3 and 5 candidates: emits exactly 3 AND
//     returns RequeueAfter > 0 (= policy.BatchRequeue).
//   - Second Reconcile: drains remaining 2 AND returns RequeueAfter == 0.
func TestReconciler_BatchBoundaryRequeues(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := mem.NewDeviceRepository()

	// Small batch policy for this test.
	smallPolicy := Policy{
		Threshold:     certRenewalTestThreshold,
		RetryInterval: 24 * time.Hour,
		BatchSize:     3,
		BatchRequeue:  10 * time.Millisecond,
	}

	// Seed 5 near-expiry certs with distinct expiry times (all within threshold).
	for i := 0; i < 5; i++ {
		id := "dev-batch-" + string(rune('a'+i))
		expiresAt := certTestBase.Add(time.Duration(i+1) * time.Hour)
		require.NoError(t, repo.Create(ctx, &domain.Device{
			ID: id, Name: id, Status: "online", LastSeen: certTestBase,
			CertEpoch: domain.DefaultCertEpoch, CertExpiresAt: expiresAt,
		}))
	}

	rec := outboxtest.NewRecorder()
	fc := clockmock.New(certTestBase)
	r, err := NewReconciler(fc, repo, rec.CellEmitter(), outbox.DemoCellTxManager(), smallPolicy, nil)
	require.NoError(t, err)

	// First Reconcile: full batch of 3.
	result1, err := r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	assert.Len(t, rec.Entries(), 3, "first reconcile emits exactly BatchSize candidates")
	assert.Greater(t, result1.RequeueAfter, time.Duration(0),
		"full batch must return RequeueAfter > 0 (= BatchRequeue)")
	assert.Equal(t, smallPolicy.BatchRequeue, result1.RequeueAfter)

	// Second Reconcile: drains remaining 2.
	result2, err := r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	assert.Len(t, rec.Entries(), 5, "second reconcile drains the remaining 2 candidates")
	assert.Equal(t, time.Duration(0), result2.RequeueAfter,
		"partial batch (< BatchSize) must return RequeueAfter == 0")
}

// failingEmitter is an outbox.Emitter whose Emit always fails, used to drive the
// error-bubble path of Reconcile/enqueueRenewal.
type failingEmitter struct{ err error }

func (f failingEmitter) Emit(context.Context, outbox.Entry) error { return f.err }

func TestReconciler_EmitFailureBubbles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := mem.NewDeviceRepository()
	seedCert(t, ctx, repo, "dev-near", certTestBase.Add(24*time.Hour))

	wantErr := errors.New("emit failed")
	r, err := NewReconciler(clockmock.New(certTestBase), repo,
		outbox.WrapEmitterForCell(failingEmitter{err: wantErr}),
		outbox.DemoCellTxManager(), certRenewalTestPolicy, nil)
	require.NoError(t, err)

	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.Error(t, err, "a failing emit must bubble out of Reconcile (loop then backs off + retries)")
	require.ErrorIs(t, err, wantErr)

	// A failed emit must NOT mark the epoch requested — the cert stays a candidate
	// so the retry re-emits.
	retryBefore := certTestBase.Add(-certRenewalTestPolicy.RetryInterval)
	got, err := repo.ListCertificateRenewalCandidates(ctx,
		certTestBase.Add(certRenewalTestThreshold), retryBefore, 100)
	require.NoError(t, err)
	assert.Len(t, got, 1, "a cert whose renewal emit failed must remain a candidate")
}

// markFailRepo wraps a mem repo and overrides MarkCertRenewalRequested to
// return a fixed error, so we can prove the error bubbles out of Reconcile.
type markFailRepo struct {
	*mem.DeviceRepository
	err error
}

func (r markFailRepo) MarkCertRenewalRequested(_ context.Context, _ string, _ int64, _ time.Time) error {
	return r.err
}

// txnScopeKey carries the ambient txnScope through ctx, mirroring how the real
// adapter routes a pgx.Tx via persistence.TxCtxKey.
type txnScopeKey struct{}

// txnScope buffers the entries emitted inside one RunInTx until it commits.
type txnScope struct{ entries []outbox.Entry }

// txnStore is a fake transactional outbox modeling ONE ambient tx shared by the
// rotate-cert emit and the devices mark — the unit-level stand-in for how the PG
// adapter routes both the outbox write and the devices UPDATE through a single
// pgx.Tx (pgexec.PGExecutor reads persistence.TxFromContext). committed holds only
// entries from closures that returned nil; a closure that errors drops its scope,
// so a failed mark rolls the buffered emit back. (mem repo + recorder are not
// themselves transactional, hence this fake.)
type txnStore struct{ committed []outbox.Entry }

func (s *txnStore) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	scope := &txnScope{}
	if err := fn(context.WithValue(ctx, txnScopeKey{}, scope)); err != nil {
		return err // rollback: the scope and its buffered emit are discarded
	}
	s.committed = append(s.committed, scope.entries...) // commit
	return nil
}

// txnEmitter enlists every Emit in the ambient txnScope; an Emit outside RunInTx
// is a wiring bug (the reconciler MUST wrap it), so it fails closed.
type txnEmitter struct{}

func (txnEmitter) Emit(ctx context.Context, e outbox.Entry) error {
	scope, ok := ctx.Value(txnScopeKey{}).(*txnScope)
	if !ok {
		return errors.New("emit outside RunInTx")
	}
	scope.entries = append(scope.entries, e)
	return nil
}

// TestReconciler_MarkFailureRollsBackEmit proves emit+mark atomicity: a failed
// MarkCertRenewalRequested rolls the rotate-cert emit back (both run on one ambient
// tx in durable mode), so NO orphan command survives and the cert stays a candidate
// for the next tick. This is a genuine regression test for F1 — under the old
// "mark after a committed emit" shape the emit would already be committed when the
// mark failed, so store.committed would be non-empty and this assertion would fail.
func TestReconciler_MarkFailureRollsBackEmit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	base := mem.NewDeviceRepository()
	seedCert(t, ctx, base, "dev-near", certTestBase.Add(24*time.Hour))

	wantErr := errors.New("mark failed")
	repo := markFailRepo{DeviceRepository: base, err: wantErr}

	store := &txnStore{}
	r, err := NewReconciler(clockmock.New(certTestBase), repo,
		outbox.WrapEmitterForCell(txnEmitter{}), persistence.WrapForCell(store),
		certRenewalTestPolicy, nil)
	require.NoError(t, err)

	_, reconcileErr := r.Reconcile(ctx, reconcile.Request{})
	require.ErrorIs(t, reconcileErr, wantErr, "mark failure must bubble out of Reconcile")
	assert.Empty(t, store.committed, "a failed mark rolls the emit back — no orphan rotate-cert command")

	// The mark never took, so the cert is still a candidate the next tick retries.
	retryBefore := certTestBase.Add(-certRenewalTestPolicy.RetryInterval)
	got, err := base.ListCertificateRenewalCandidates(ctx,
		certTestBase.Add(certRenewalTestThreshold), retryBefore, 100)
	require.NoError(t, err)
	assert.Len(t, got, 1, "the cert remains a candidate after a rolled-back renewal")
}

// TestReconciler_EmitAndMarkCommitTogether is the commit-side companion (and
// anti-vacuity guard) for the rollback test above: when the mark succeeds the
// buffered emit IS committed and the cert leaves the candidate set.
func TestReconciler_EmitAndMarkCommitTogether(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := mem.NewDeviceRepository()
	seedCert(t, ctx, repo, "dev-near", certTestBase.Add(24*time.Hour))

	store := &txnStore{}
	r, err := NewReconciler(clockmock.New(certTestBase), repo,
		outbox.WrapEmitterForCell(txnEmitter{}), persistence.WrapForCell(store),
		certRenewalTestPolicy, nil)
	require.NoError(t, err)

	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	assert.Len(t, store.committed, 1, "a successful mark commits the buffered emit")

	retryBefore := certTestBase.Add(-certRenewalTestPolicy.RetryInterval)
	got, err := repo.ListCertificateRenewalCandidates(ctx,
		certTestBase.Add(certRenewalTestThreshold), retryBefore, 100)
	require.NoError(t, err)
	assert.Empty(t, got, "the committed mark removes the cert from the candidate set")
}

func TestNewReconciler_Validation(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(certTestBase)
	repo := mem.NewDeviceRepository()
	rec := outboxtest.NewRecorder()

	_, err := NewReconciler(fc, nil, rec.CellEmitter(), outbox.DemoCellTxManager(), certRenewalTestPolicy, nil)
	require.Error(t, err, "nil repo must fail-fast")

	_, err = NewReconciler(fc, repo, nil, outbox.DemoCellTxManager(), certRenewalTestPolicy, nil)
	require.Error(t, err, "nil emitter must fail-fast")

	_, err = NewReconciler(fc, repo, rec.CellEmitter(), nil, certRenewalTestPolicy, nil)
	require.Error(t, err, "nil txRunner must fail-fast")

	// Policy validation: each non-positive field must fail-fast.
	_, err = NewReconciler(fc, repo, rec.CellEmitter(), outbox.DemoCellTxManager(),
		Policy{Threshold: 0, RetryInterval: time.Hour, BatchSize: 10, BatchRequeue: time.Second}, nil)
	require.Error(t, err, "non-positive Threshold must fail-fast")

	_, err = NewReconciler(fc, repo, rec.CellEmitter(), outbox.DemoCellTxManager(),
		Policy{Threshold: time.Hour, RetryInterval: 0, BatchSize: 10, BatchRequeue: time.Second}, nil)
	require.Error(t, err, "non-positive RetryInterval must fail-fast")

	_, err = NewReconciler(fc, repo, rec.CellEmitter(), outbox.DemoCellTxManager(),
		Policy{Threshold: time.Hour, RetryInterval: time.Hour, BatchSize: 0, BatchRequeue: time.Second}, nil)
	require.Error(t, err, "non-positive BatchSize must fail-fast")

	_, err = NewReconciler(fc, repo, rec.CellEmitter(), outbox.DemoCellTxManager(),
		Policy{Threshold: time.Hour, RetryInterval: time.Hour, BatchSize: 10, BatchRequeue: 0}, nil)
	require.Error(t, err, "non-positive BatchRequeue must fail-fast")

	// Valid policy passes.
	_, err = NewReconciler(fc, repo, rec.CellEmitter(), outbox.DemoCellTxManager(),
		certRenewalTestPolicy, nil)
	require.NoError(t, err, "valid policy must not fail-fast")
}
