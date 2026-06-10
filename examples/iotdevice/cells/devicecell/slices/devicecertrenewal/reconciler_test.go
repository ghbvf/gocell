package devicecertrenewal

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecert"
	cmdenqueue "github.com/ghbvf/gocell/generated/contracts/command/devicecommand/enqueue/v1"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/kernel/reconcile"
	rtcommand "github.com/ghbvf/gocell/runtime/command"
)

var certTestBase = time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)

const certRenewalTestThreshold = 7 * 24 * time.Hour

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

func newTestReconciler(t *testing.T, fc *clockmock.FakeClock, store *devicecert.Store, rec *outboxtest.Recorder) *Reconciler {
	t.Helper()
	r, err := NewReconciler(fc, store, rec.CellEmitter(), outbox.DemoCellTxManager(),
		certRenewalTestThreshold, nil)
	require.NoError(t, err)
	return r
}

func TestReconciler_EmitsRenewalForNearExpiry(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := devicecert.NewStore()
	// dev-near expires within the threshold; dev-far is well outside it.
	_, err := store.Issue(ctx, "dev-near", certTestBase.Add(24*time.Hour))
	require.NoError(t, err)
	_, err = store.Issue(ctx, "dev-far", certTestBase.Add(30*24*time.Hour))
	require.NoError(t, err)

	rec := outboxtest.NewRecorder()
	r := newTestReconciler(t, clockmock.New(certTestBase), store, rec)

	_, err = r.Reconcile(ctx, reconcile.Request{})
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
	store := devicecert.NewStore()
	_, err := store.Issue(ctx, "dev-far", certTestBase.Add(30*24*time.Hour))
	require.NoError(t, err)

	rec := outboxtest.NewRecorder()
	r := newTestReconciler(t, clockmock.New(certTestBase), store, rec)

	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	assert.Empty(t, rec.Entries(), "no near-expiry cert -> no command emitted")
}

// TestReconciler_DedupsAcrossTicksPerEpoch is the F1 invariant proven WITHOUT a
// relay: after the first emit the store marks the epoch renewal-requested, so the
// second tick's ScanNearExpiry skips it. The per-epoch single-emit therefore does
// NOT depend on the relay's 24h command-done TTL at all.
func TestReconciler_DedupsAcrossTicksPerEpoch(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := devicecert.NewStore()
	_, err := store.Issue(ctx, "dev-near", certTestBase.Add(24*time.Hour))
	require.NoError(t, err)

	rec := outboxtest.NewRecorder()
	r := newTestReconciler(t, clockmock.New(certTestBase), store, rec)

	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)

	assert.Len(t, rec.Entries(), 1, "one emit per epoch across ticks (store-level dedup, no TTL)")
}

// TestReconciler_ReissueReemitsAfterMark proves the forcing function: a
// post-rotation re-issue advances the epoch, which is not renewal-requested, so a
// fresh rotate-cert command dispatches for the new epoch.
func TestReconciler_ReissueReemitsAfterMark(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := devicecert.NewStore()
	_, err := store.Issue(ctx, "dev-near", certTestBase.Add(24*time.Hour))
	require.NoError(t, err)

	rec := outboxtest.NewRecorder()
	r := newTestReconciler(t, clockmock.New(certTestBase), store, rec)

	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)
	require.Len(t, rec.Entries(), 1)

	// Re-issue (still near-expiry) advances to epoch 2.
	_, err = store.Issue(ctx, "dev-near", certTestBase.Add(12*time.Hour))
	require.NoError(t, err)

	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.NoError(t, err)

	entries := rec.Entries()
	require.Len(t, entries, 2, "an epoch advance re-dispatches")
	assert.Equal(t, rotateCommandID("dev-near", 2), entries[1].Metadata()[rtcommand.CommandIDMetadataKey],
		"the second command carries the new epoch's id")
}

// failingEmitter is an outbox.Emitter whose Emit always fails, used to drive the
// error-bubble path of Reconcile/enqueueRenewal.
type failingEmitter struct{ err error }

func (f failingEmitter) Emit(context.Context, outbox.Entry) error { return f.err }

func TestReconciler_EmitFailureBubbles(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := devicecert.NewStore()
	_, err := store.Issue(ctx, "dev-near", certTestBase.Add(24*time.Hour))
	require.NoError(t, err)

	wantErr := errors.New("emit failed")
	r, err := NewReconciler(clockmock.New(certTestBase), store,
		outbox.WrapEmitterForCell(failingEmitter{err: wantErr}),
		outbox.DemoCellTxManager(), certRenewalTestThreshold, nil)
	require.NoError(t, err)

	_, err = r.Reconcile(ctx, reconcile.Request{})
	require.Error(t, err, "a failing emit must bubble out of Reconcile (loop then backs off + retries)")
	require.ErrorIs(t, err, wantErr)

	// A failed emit must NOT mark the epoch requested — the cert stays scannable so
	// the retry re-emits.
	got, err := store.ScanNearExpiry(ctx, certTestBase.Add(certRenewalTestThreshold))
	require.NoError(t, err)
	assert.Len(t, got, 1, "a cert whose renewal emit failed must remain scannable")
}

func TestNewReconciler_Validation(t *testing.T) {
	t.Parallel()
	fc := clockmock.New(certTestBase)
	store := devicecert.NewStore()
	rec := outboxtest.NewRecorder()

	_, err := NewReconciler(fc, nil, rec.CellEmitter(), outbox.DemoCellTxManager(), certRenewalTestThreshold, nil)
	require.Error(t, err, "nil store must fail-fast")

	_, err = NewReconciler(fc, store, nil, outbox.DemoCellTxManager(), certRenewalTestThreshold, nil)
	require.Error(t, err, "nil emitter must fail-fast")

	_, err = NewReconciler(fc, store, rec.CellEmitter(), nil, certRenewalTestThreshold, nil)
	require.Error(t, err, "nil txRunner must fail-fast")

	_, err = NewReconciler(fc, store, rec.CellEmitter(), outbox.DemoCellTxManager(), 0, nil)
	require.Error(t, err, "non-positive threshold must fail-fast")
}
