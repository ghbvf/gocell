package auditverify

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

var testHMACKey = []byte("test-hmac-key-32bytes-long!!!!!!!")

func newTestProtocol(t testing.TB) *ledger.Protocol {
	t.Helper()
	ns, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err)
	p, err := ledger.NewProtocol(
		ledger.WithChainHMAC(testHMACKey),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err)
	return p
}

func newTestService(t testing.TB) (*Service, *ledger.MemStore) {
	t.Helper()
	p := newTestProtocol(t)
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	svc, err := NewService(store, slog.Default(), WithTxManager(&stubTxRunner{}))
	require.NoError(t, err)
	return svc, store
}

func TestNewService_TxRunnerRequired(t *testing.T) {
	p := newTestProtocol(t)
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	_, err = NewService(store, slog.Default() /* no WithTxManager */)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, err.Error(), "TxRunner required")
}

func TestService_VerifyChain_Empty(t *testing.T) {
	svc, store := newTestService(t)
	// Seed one entry so fromSeq=1 toSeq=1 is valid.
	p := newTestProtocol(t)
	e := &ledger.Entry{
		EventID:   "evt-0",
		EventType: "event.test",
		ActorID:   "actor-1",
		Timestamp: clock.Real().Now(),
		Payload:   []byte(`{}`),
	}
	require.NoError(t, store.Append(context.Background(), e))

	result, err := svc.VerifyChain(context.Background(), 1, 1)
	require.NoError(t, err)
	assert.True(t, result.Valid)
	_ = p
}

func TestService_VerifyChain_ValidEntries(t *testing.T) {
	svc, store := newTestService(t)

	for i := range 3 {
		e := &ledger.Entry{
			EventID:   "evt-" + string(rune('0'+i)),
			EventType: "event.test",
			ActorID:   "actor-1",
			Timestamp: clock.Real().Now(),
			Payload:   []byte(`{"i":` + string(rune('0'+i)) + `}`),
		}
		require.NoError(t, store.Append(context.Background(), e))
	}

	result, err := svc.VerifyChain(context.Background(), 1, 3)
	require.NoError(t, err)
	assert.True(t, result.Valid)
	// F25: valid chain must return FirstInvalidSeq == -1 (sentinel from store).
	assert.Equal(t, int64(-1), result.FirstInvalidSeq)
}

func TestService_VerifyChain_InvalidRange_Error(t *testing.T) {
	svc, _ := newTestService(t)
	// toSeq < fromSeq → error from store
	_, err := svc.VerifyChain(context.Background(), 5, 1)
	require.Error(t, err)
}
