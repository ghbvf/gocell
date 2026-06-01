package auditappendbootstrap_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/auditcore/slices/auditappendbootstrap"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/audit"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
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
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	bs, err := audit.NewBootstrapLedgerStore(store)
	require.NoError(t, err)
	return bs
}

func mustNewEntry(t *testing.T, payload []byte) outbox.Entry {
	t.Helper()
	entry, err := outbox.NewEntry(clock.Real(), context.Background(),
		"event.auth.bootstrap-failed.v1", payload)
	require.NoError(t, err)
	return entry
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
	svc, err := auditappendbootstrap.NewService(clock.Real())
	require.NoError(t, err)
	require.NotNil(t, svc)
}

func TestNewService_WithStore_OK(t *testing.T) {
	bs := newTestBootstrapStore(t)
	svc, err := auditappendbootstrap.NewService(clock.Real(),
		auditappendbootstrap.WithBootstrapStore(bs))
	require.NoError(t, err)
	require.NotNil(t, svc)
}

// --- HandleEvent tests ------------------------------------------------------

func TestHandleEvent_NoStore_Requeues(t *testing.T) {
	// When no bootstrap store is configured, HandleEvent must Requeue (not Reject).
	svc, err := auditappendbootstrap.NewService(clock.Real())
	require.NoError(t, err)

	payload, _ := json.Marshal(map[string]string{"reason": "rate_limited"})
	entry := mustNewEntry(t, payload)
	result := svc.HandleEvent(context.Background(), entry)
	assert.Equal(t, outbox.DispositionRequeue, result.Disposition)
}

func TestHandleEvent_ValidReasons_Ack(t *testing.T) {
	bs := newTestBootstrapStore(t)
	svc, err := auditappendbootstrap.NewService(clock.Real(),
		auditappendbootstrap.WithBootstrapStore(bs))
	require.NoError(t, err)

	for _, reason := range []string{"missing_header", "wrong_credentials", "rate_limited"} {
		reason := reason
		t.Run(reason, func(t *testing.T) {
			payload, err := json.Marshal(map[string]string{"reason": reason, "clientIp": "1.2.3.4"})
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
	svc, err := auditappendbootstrap.NewService(clock.Real(),
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
	svc, err := auditappendbootstrap.NewService(clock.Real(),
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

func TestHandleEvent_UnknownReason_Requeues(t *testing.T) {
	// Unknown reason goes to AppendBootstrapAuthFail which rejects it with ErrValidationFailed.
	// AppendBootstrapAuthFail returns an error (not permanent) so HandleEvent Requeues.
	bs := newTestBootstrapStore(t)
	svc, err := auditappendbootstrap.NewService(clock.Real(),
		auditappendbootstrap.WithBootstrapStore(bs))
	require.NoError(t, err)

	payload, _ := json.Marshal(map[string]string{"reason": "unknown_reason"})
	entry := mustNewEntry(t, payload)
	result := svc.HandleEvent(context.Background(), entry)
	// AppendBootstrapAuthFail rejects unknown reasons with an error, which HandleEvent wraps as Requeue.
	assert.Equal(t, outbox.DispositionRequeue, result.Disposition)
}
