package auditappendbootstrap_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/corecells/auditcore/slices/auditappendbootstrap"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
	"github.com/ghbvf/gocell/framework/runtime/audit"
	"github.com/ghbvf/gocell/framework/runtime/audit/ledger"
)

var testHMACKey = []byte("test-hmac-key-32bytes-long!!!!!!!")

func newTestBootstrapStore(t *testing.T) *audit.BootstrapLedgerStore {
	t.Helper()
	ns := audit.BootstrapNamespace()
	p, err := ledger.NewProtocol(
		ns,
		testHMACKey,
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err)
	// F21: use a deterministic clockmock instead of clock.Real() to avoid
	// wall-clock dependencies in the test.
	fc := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	store, err := ledger.NewMemStore(p, fc)
	require.NoError(t, err)
	bs, err := audit.NewBootstrapLedgerStore(store)
	require.NoError(t, err)
	return bs
}

// testClock returns a deterministic fake clock for use in tests (F21).
func testClock() *clockmock.FakeClock {
	return clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
}

func mustNewEntry(t *testing.T, payload []byte) outbox.Entry {
	t.Helper()
	// F21: use a deterministic clockmock instead of clock.Real().
	entry, err := outbox.NewEntry(testClock(), context.Background(),
		"event.auth.bootstrap-failed.v1", payload)
	require.NoError(t, err)
	return entry
}

// newErrAppendBootstrapStore creates a *audit.BootstrapLedgerStore backed by
// an errLedgerStore that always returns appendErr on Append. Used by F17 to
// test the transient-append-failure → Requeue path.
func newErrAppendBootstrapStore(t *testing.T, appendErr error) *audit.BootstrapLedgerStore {
	t.Helper()
	ns := audit.BootstrapNamespace()
	p, nerr := ledger.NewProtocol(
		ns,
		testHMACKey,
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, nerr)
	inner, nerr := ledger.NewMemStore(p, testClock())
	require.NoError(t, nerr)
	bs, nerr := audit.NewBootstrapLedgerStore(&errLedgerStore{inner: inner, appendErr: appendErr})
	require.NoError(t, nerr)
	return bs
}

// errLedgerStore wraps a ledger.Store and injects an error on Append.
// Implements ledger.Store for test use by delegating all other methods.
type errLedgerStore struct {
	inner     ledger.Store
	appendErr error
}

func (e *errLedgerStore) Protocol() *ledger.Protocol { return e.inner.Protocol() }

func (e *errLedgerStore) Append(ctx context.Context, entry *ledger.Entry) error {
	if e.appendErr != nil {
		return e.appendErr
	}
	return e.inner.Append(ctx, entry)
}

func (e *errLedgerStore) Tail(ctx context.Context) (ledger.TailSnapshot, error) {
	return e.inner.Tail(ctx)
}

func (e *errLedgerStore) GetBySeq(ctx context.Context, vis tenant.RowVisibility, seq int64) (*ledger.Entry, error) {
	return e.inner.GetBySeq(ctx, vis, seq)
}

func (e *errLedgerStore) GetByID(ctx context.Context, t tenant.TenantID, vis tenant.RowVisibility, id string) (*ledger.Entry, error) {
	return e.inner.GetByID(ctx, t, vis, id)
}

func (e *errLedgerStore) Query(
	ctx context.Context, t tenant.TenantID, vis tenant.RowVisibility, filters ledger.AuditFilters, params query.ListParams,
) ([]*ledger.Entry, error) {
	return e.inner.Query(ctx, t, vis, filters, params)
}

func (e *errLedgerStore) Verify(ctx context.Context, fromSeq, toSeq int64) (bool, int64, error) {
	return e.inner.Verify(ctx, fromSeq, toSeq)
}

func (e *errLedgerStore) RepoReady(ctx context.Context) error {
	return e.inner.RepoReady(ctx)
}

// --- NewService tests -------------------------------------------------------

func TestNewService_NilClock_Error(t *testing.T) {
	// clock.MustHaveClock panics on nil — the test verifies the constructor
	// panics rather than silently accepts nil (programmer-error convention per go-standards.md).
	assert.Panics(t, func() {
		_, _ = auditappendbootstrap.NewService(nil)
	})
}

func TestNewService_NoStore_OK(t *testing.T) {
	// No-op mode: no store wired. Service must be created successfully.
	svc, err := auditappendbootstrap.NewService(testClock())
	require.NoError(t, err)
	require.NotNil(t, svc)
}

func TestNewService_WithStore_OK(t *testing.T) {
	bs := newTestBootstrapStore(t)
	svc, err := auditappendbootstrap.NewService(testClock(),
		auditappendbootstrap.WithBootstrapStore(bs))
	require.NoError(t, err)
	require.NotNil(t, svc)
}

// --- HandleEvent tests ------------------------------------------------------

func TestHandleEvent_NoStore_Rejects(t *testing.T) {
	// C4: When no bootstrap store is configured, HandleEvent must Reject (not Requeue).
	// A nil store is a permanent misconfiguration — retrying burns the budget.
	svc, err := auditappendbootstrap.NewService(testClock())
	require.NoError(t, err)

	payload, _ := json.Marshal(map[string]string{"reason": "rate_limited"})
	entry := mustNewEntry(t, payload)
	result := svc.HandleEvent(context.Background(), entry)
	assert.Equal(t, outbox.DispositionReject, result.Disposition,
		"nil store must Reject (permanent misconfiguration, not transient)")
	require.NotNil(t, result.Err, "Reject must carry an error")
	var permErr *outbox.PermanentError
	assert.ErrorAs(t, result.Err, &permErr, "nil store must be a permanent error")
}

func TestHandleEvent_ValidReasons_Ack(t *testing.T) {
	bs := newTestBootstrapStore(t)
	svc, err := auditappendbootstrap.NewService(testClock(),
		auditappendbootstrap.WithBootstrapStore(bs))
	require.NoError(t, err)

	for _, reason := range []string{"missing_header", "wrong_credentials", "rate_limited"} {
		reason := reason
		t.Run(reason, func(t *testing.T) {
			payload, err := json.Marshal(map[string]string{"reason": reason, "clientIpHash": strings.Repeat("a1b2c3d4", 8)})
			require.NoError(t, err)
			entry := mustNewEntry(t, payload)
			result := svc.HandleEvent(context.Background(), entry)
			assert.Equal(t, outbox.DispositionAck, result.Disposition,
				"valid reason %q must Ack", reason)
		})
	}
}

func TestHandleEvent_InvalidJSON_Rejects(t *testing.T) {
	bs := newTestBootstrapStore(t)
	svc, err := auditappendbootstrap.NewService(testClock(),
		auditappendbootstrap.WithBootstrapStore(bs))
	require.NoError(t, err)

	entry := mustNewEntry(t, []byte("not valid json"))
	result := svc.HandleEvent(context.Background(), entry)
	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	require.NotNil(t, result.Err, "Reject must carry an error")
	var permErr *outbox.PermanentError
	assert.ErrorAs(t, result.Err, &permErr, "invalid JSON must be a permanent error")
}

func TestHandleEvent_EmptyReason_Rejects(t *testing.T) {
	bs := newTestBootstrapStore(t)
	svc, err := auditappendbootstrap.NewService(testClock(),
		auditappendbootstrap.WithBootstrapStore(bs))
	require.NoError(t, err)

	payload, _ := json.Marshal(map[string]string{"reason": ""})
	entry := mustNewEntry(t, payload)
	result := svc.HandleEvent(context.Background(), entry)
	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	require.NotNil(t, result.Err, "Reject must carry an error")
	var permErr *outbox.PermanentError
	assert.ErrorAs(t, result.Err, &permErr, "empty reason must be a permanent error")
}

func TestHandleEvent_UnknownReason_Rejects(t *testing.T) {
	// Unknown reason passes through to AppendBootstrapAuthFail which returns
	// ErrValidationFailed (KindInvalid / IsExpected4xx). This is a permanent
	// schema violation — HandleEvent must Reject (DLX), not Requeue.
	bs := newTestBootstrapStore(t)
	svc, err := auditappendbootstrap.NewService(testClock(),
		auditappendbootstrap.WithBootstrapStore(bs))
	require.NoError(t, err)

	payload, _ := json.Marshal(map[string]string{"reason": "unknown_reason"})
	entry := mustNewEntry(t, payload)
	result := svc.HandleEvent(context.Background(), entry)
	assert.Equal(t, outbox.DispositionReject, result.Disposition,
		"unknown reason must Reject (permanent schema violation, not retryable)")
	require.NotNil(t, result.Err, "Reject must carry an error")
	var permErr *outbox.PermanentError
	assert.ErrorAs(t, result.Err, &permErr, "unknown reason must be a permanent error")
}

// F17: transient append error → Requeue.
func TestHandleEvent_TransientAppendError_Requeues(t *testing.T) {
	// A non-validation error from AppendBootstrapAuthFail (e.g. DB write failure)
	// must Requeue — ConsumerBase will retry with backoff.
	transientErr := fmt.Errorf("db: connection refused")
	bs := newErrAppendBootstrapStore(t, transientErr)
	svc, err := auditappendbootstrap.NewService(testClock(),
		auditappendbootstrap.WithBootstrapStore(bs))
	require.NoError(t, err)

	payload, _ := json.Marshal(map[string]string{"reason": "rate_limited"})
	entry := mustNewEntry(t, payload)
	result := svc.HandleEvent(context.Background(), entry)
	assert.Equal(t, outbox.DispositionRequeue, result.Disposition,
		"transient infra error must Requeue for retry")
	require.NotNil(t, result.Err, "Requeue must carry the original error")
	assert.False(t, errors.As(result.Err, new(*outbox.PermanentError)),
		"transient error must NOT be wrapped in PermanentError")
}

// F18 (C1/F2): idempotency / duplicate-delivery — redelivering the SAME outbox
// entry must Ack both times AND leave exactly one ledger row. The ledger keys
// idempotency on EventID (= entry.ID()); because HandleEvent now forwards the
// stable entry.ID() (not a per-call uuid), the second Append collapses to
// ErrAuditLedgerAlreadyExists which HandleEvent treats as a replay Ack.
// Asserting EntryCount==1 is the regression guard: minting a fresh EventID per
// call (the prior bug) would Ack twice but persist two rows — double-counting
// the same auth failure in the compliance ledger.
func TestHandleEvent_DuplicateDelivery_BothAck(t *testing.T) {
	bs := newTestBootstrapStore(t)
	svc, err := auditappendbootstrap.NewService(testClock(),
		auditappendbootstrap.WithBootstrapStore(bs))
	require.NoError(t, err)

	payload, _ := json.Marshal(map[string]string{"reason": "wrong_credentials", "clientIpHash": strings.Repeat("deadbeef", 8)})
	entry := mustNewEntry(t, payload)

	result1 := svc.HandleEvent(context.Background(), entry)
	assert.Equal(t, outbox.DispositionAck, result1.Disposition, "first delivery must Ack")

	result2 := svc.HandleEvent(context.Background(), entry)
	assert.Equal(t, outbox.DispositionAck, result2.Disposition,
		"duplicate delivery must Ack (idempotent replay via stable EventID)")

	snap, terr := bs.Tail(context.Background())
	require.NoError(t, terr)
	assert.Equal(t, int64(1), snap.EntryCount,
		"redelivery must NOT double-write: exactly one ledger entry expected")
}

// F19: Reject path (nil store and invalid-JSON) must carry an error whose inner
// errcode has code ErrValidationFailed — pinning the inner code so callers
// relying on errcode.IsExpected4xx get consistent classification.
func TestHandleEvent_RejectPins_ErrValidationFailed(t *testing.T) {
	svc, err := auditappendbootstrap.NewService(testClock())
	require.NoError(t, err)

	payload, _ := json.Marshal(map[string]string{"reason": "rate_limited"})
	entry := mustNewEntry(t, payload)
	result := svc.HandleEvent(context.Background(), entry)
	require.Equal(t, outbox.DispositionReject, result.Disposition)

	// The inner error wrapped in PermanentError must be an *errcode.Error with
	// ErrValidationFailed — callers use this to classify permanent errors.
	var permErr *outbox.PermanentError
	require.ErrorAs(t, result.Err, &permErr)
	var ec *errcode.Error
	require.ErrorAs(t, permErr.Unwrap(), &ec, "inner error must be *errcode.Error")
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code,
		"permanent error must pin ErrValidationFailed code")
}

// Ensure clock package is used (suppress any unused-import lint errors).
var _ clock.Clock = testClock()
