package devicecertrenewal

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/framework/kernel/reconcile"
	rtcommand "github.com/ghbvf/gocell/framework/runtime/command"
	cmdenqueue "github.com/ghbvf/gocell/generated/contracts/command/devicecommand/enqueue/v1"
)

var certTestBase = time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)

const certRenewalTestThreshold = 7 * 24 * time.Hour

// certRenewalTestAttemptTTL is the AttemptTTL used in unit tests.
const certRenewalTestAttemptTTL = 36 * time.Hour

// certRenewalTestPolicy is the standard test policy. The stateless producer
// sweeps ALL near-expiry certs per tick — no BatchSize or BatchRequeue needed.
var certRenewalTestPolicy = Policy{
	Threshold:  certRenewalTestThreshold,
	AttemptTTL: certRenewalTestAttemptTTL,
}

// seedCert persists a device with the given cert state into a mem repo.
func seedCert(t *testing.T, ctx context.Context, repo domain.DeviceRepository, id string, expiresAt time.Time) {
	t.Helper()
	require.NoError(t, repo.Create(ctx, &domain.Device{
		ID: id, Name: id, Status: "online", LastSeen: certTestBase,
		CertEpoch: domain.DefaultCertEpoch, CertExpiresAt: expiresAt,
	}))
}

func newTestReconciler(t *testing.T, fc *clockmock.FakeClock, repo domain.DeviceRepository, rec *outboxtest.Recorder) *Reconciler {
	t.Helper()
	r, err := NewReconciler(fc, repo, rec.CellEmitter(), certRenewalTestPolicy, nil)
	require.NoError(t, err)
	return r
}

func TestRotateCommandID(t *testing.T) {
	t.Parallel()
	// Stable for the same (deviceID, epoch): active-uniqueness relies on it.
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

// TestReconciler_EmitsWithActiveUniqueness verifies that each emitted rotate-cert
// command carries the CommandDeadlineMetadataKey metadata set to a deadline
// approximately now+AttemptTTL. This is the WithActiveUniqueness contract: the
// relay reads that key to inject (claimKey, deadline) into ctx so the queue
// enforces active-uniqueness (F1 fix, #1820).
func TestReconciler_EmitsWithActiveUniqueness(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := mem.NewDeviceRepository()
	seedCert(t, ctx, repo, "dev-near", certTestBase.Add(24*time.Hour))

	rec := outboxtest.NewRecorder()
	fc := clockmock.New(certTestBase)
	r := newTestReconciler(t, fc, repo, rec)

	_, err := r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	require.Len(t, rec.Entries(), 1)

	got := rec.Entries()[0]
	deadlineRaw, ok := got.Metadata()[rtcommand.CommandDeadlineMetadataKey]
	require.True(t, ok, "emitted entry must carry CommandDeadlineMetadataKey (WithActiveUniqueness)")
	require.NotEmpty(t, deadlineRaw, "deadline metadata must be non-empty")

	// Parse back and check it's approximately now+AttemptTTL.
	deadline, parseErr := time.Parse(time.RFC3339Nano, deadlineRaw)
	require.NoError(t, parseErr)
	wantDeadline := certTestBase.Add(certRenewalTestAttemptTTL)
	assert.WithinDuration(t, wantDeadline, deadline, time.Second,
		"OverallDeadline must be now+AttemptTTL")
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

// TestReconciler_ReemitsForNewEpoch proves that when a device's cert_epoch has
// advanced (post-rotation re-issue), the reconciler emits a fresh commandID for
// the new epoch. The new epoch yields a different commandID, so active-uniqueness
// treats it as a distinct command and admits it even if the prior epoch's command
// is still non-terminal.
func TestReconciler_ReemitsForNewEpoch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := mem.NewDeviceRepository()
	// epoch 2, near expiry.
	require.NoError(t, repo.Create(ctx, &domain.Device{
		ID: "dev-near", Name: "dev-near", Status: "online", LastSeen: certTestBase,
		CertEpoch: 2, CertExpiresAt: certTestBase.Add(12 * time.Hour),
	}))

	rec := outboxtest.NewRecorder()
	r := newTestReconciler(t, clockmock.New(certTestBase), repo, rec)

	_, err := r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)

	entries := rec.Entries()
	require.Len(t, entries, 1, "one rotate-cert command emitted")
	assert.Equal(t, rotateCommandID("dev-near", 2), entries[0].Metadata()[rtcommand.CommandIDMetadataKey],
		"the command carries the epoch's id")
}

// TestReconciler_StatelessEmitsOnEachTick proves the stateless producer emits
// on every tick (no mark suppression). The queue active-uniqueness (not the
// producer) coalesces duplicates. Two ticks with the same near-expiry device
// both produce an emit — the queue decides whether to admit.
func TestReconciler_StatelessEmitsOnEachTick(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := mem.NewDeviceRepository()
	seedCert(t, ctx, repo, "dev-near", certTestBase.Add(24*time.Hour))

	rec := outboxtest.NewRecorder()
	fc := clockmock.New(certTestBase)
	r := newTestReconciler(t, fc, repo, rec)

	_, err := r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	require.Len(t, rec.Entries(), 1, "first tick emits one command")

	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	assert.Len(t, rec.Entries(), 2,
		"second tick also emits — stateless producer, queue owns dedup via active-uniqueness")

	// Both ticks must carry the SAME CommandIDMetadataKey per device+epoch: the
	// active-uniqueness key is deterministic (rotateCommandID) and stable across
	// ticks. If the key drifted between ticks the queue would admit a second
	// command instead of coalescing, breaking the F1 guarantee.
	tick1Key := rec.Entries()[0].Metadata()[rtcommand.CommandIDMetadataKey]
	tick2Key := rec.Entries()[1].Metadata()[rtcommand.CommandIDMetadataKey]
	assert.NotEmpty(t, tick1Key, "first tick must carry CommandIDMetadataKey")
	assert.Equal(t, tick1Key, tick2Key,
		"CommandIDMetadataKey must be identical across ticks for the same (device,epoch)")
}

// TestReconciler_FullSweepPerTick proves that a single Reconcile tick emits for
// ALL near-expiry certs regardless of count — no LIMIT truncation, no
// RequeueAfter. This is the core full-sweep invariant of the stateless producer.
func TestReconciler_FullSweepPerTick(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := mem.NewDeviceRepository()

	// Seed more than a page-sized batch to prove no LIMIT truncation occurs.
	// (The old BatchSize=100 design was removed; 12 is well above any small
	// default and keeps the test fast while being a meaningful falsifier.)
	const totalCerts = 12
	for i := 0; i < totalCerts; i++ {
		id := fmt.Sprintf("dev-sweep-%02d", i)
		expiresAt := certTestBase.Add(time.Duration(i+1) * time.Hour)
		require.NoError(t, repo.Create(ctx, &domain.Device{
			ID: id, Name: id, Status: "online", LastSeen: certTestBase,
			CertEpoch: domain.DefaultCertEpoch, CertExpiresAt: expiresAt,
		}))
	}

	rec := outboxtest.NewRecorder()
	fc := clockmock.New(certTestBase)
	r, err := NewReconciler(fc, repo, rec.CellEmitter(), certRenewalTestPolicy, nil)
	require.NoError(t, err)

	result, err := r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	assert.Len(t, rec.Entries(), totalCerts, "all near-expiry certs emitted in one tick")
	assert.Equal(t, time.Duration(0), result.RequeueAfter,
		"full-sweep returns no RequeueAfter — ticker is the sole pacing mechanism")
}

// perDeviceEmitter is an outbox.Emitter that succeeds for device IDs not in
// failIDs and returns failErr for those that are. It inspects the AggregateID
// of each emitted entry to identify the device.
type perDeviceEmitter struct {
	inner   outbox.CellEmitter
	failIDs map[string]bool
	failErr error
}

func (e *perDeviceEmitter) Emit(ctx context.Context, entry outbox.Entry) error {
	if e.failIDs[entry.AggregateID()] {
		return e.failErr
	}
	return e.inner.Emit(ctx, entry)
}

// TestReconciler_PartialBatchFailureRetriesOnlyFailed seeds 2 near-expiry certs
// (device A and B). The first Reconcile uses an emitter that succeeds for A but
// fails for B → error bubbles (B failed), A is emitted. On the second Reconcile
// both candidates are seen again (stateless scan); B's emitter succeeds on the
// second pass → B emits.
func TestReconciler_PartialBatchFailureRetriesOnlyFailed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	repo := mem.NewDeviceRepository()

	seedCert(t, ctx, repo, "dev-a", certTestBase.Add(12*time.Hour))
	seedCert(t, ctx, repo, "dev-b", certTestBase.Add(18*time.Hour))

	wantErr := errors.New("emit failed for dev-b")
	rec := outboxtest.NewRecorder()
	// First pass: fail dev-b, succeed dev-a.
	failEmitter := &perDeviceEmitter{
		inner:   rec.CellEmitter(),
		failIDs: map[string]bool{"dev-b": true},
		failErr: wantErr,
	}

	fc := clockmock.New(certTestBase)
	r, err := NewReconciler(fc, repo, outbox.WrapEmitterForCell(failEmitter), certRenewalTestPolicy, nil)
	require.NoError(t, err)

	// First Reconcile: candidates are scanned in expiry ASC order: dev-a first,
	// then dev-b. dev-a emits, dev-b fails → error bubbles.
	_, reconcileErr := r.Reconcile(ctx, reconcile.Request{})
	require.ErrorIs(t, reconcileErr, wantErr, "error from dev-b must bubble out of Reconcile")
	require.Len(t, rec.Entries(), 1, "dev-a was emitted before dev-b errored")
	assert.Equal(t, "dev-a", rec.Entries()[0].AggregateID(), "the single emit is for dev-a")

	// Wire a succeeding emitter for the second pass.
	rec2 := outboxtest.NewRecorder()
	r2, err := NewReconciler(fc, repo, rec2.CellEmitter(), certRenewalTestPolicy, nil)
	require.NoError(t, err)

	_, err = r2.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)

	entries := rec2.Entries()
	require.Len(t, entries, 2, "second Reconcile emits both dev-a and dev-b (stateless rescan)")
	deviceIDs := map[string]bool{entries[0].AggregateID(): true, entries[1].AggregateID(): true}
	assert.True(t, deviceIDs["dev-b"], "dev-b must be emitted on the second Reconcile")
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
		certRenewalTestPolicy, nil)
	require.NoError(t, err)

	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.Error(t, err, "a failing emit must bubble out of Reconcile (loop then backs off + retries)")
	require.ErrorIs(t, err, wantErr)
}

func TestNewReconciler_Validation(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(certTestBase)
	repo := mem.NewDeviceRepository()
	rec := outboxtest.NewRecorder()

	_, err := NewReconciler(fc, nil, rec.CellEmitter(), certRenewalTestPolicy, nil)
	require.Error(t, err, "nil repo must fail-fast")

	_, err = NewReconciler(fc, repo, nil, certRenewalTestPolicy, nil)
	require.Error(t, err, "nil emitter must fail-fast")

	// Policy validation: each non-positive field must fail-fast.
	_, err = NewReconciler(fc, repo, rec.CellEmitter(),
		Policy{Threshold: 0, AttemptTTL: time.Hour}, nil)
	require.Error(t, err, "non-positive Threshold must fail-fast")

	_, err = NewReconciler(fc, repo, rec.CellEmitter(),
		Policy{Threshold: time.Hour, AttemptTTL: 0}, nil)
	require.Error(t, err, "non-positive AttemptTTL must fail-fast")

	// Valid policy passes.
	_, err = NewReconciler(fc, repo, rec.CellEmitter(), certRenewalTestPolicy, nil)
	require.NoError(t, err, "valid policy must not fail-fast")
}
