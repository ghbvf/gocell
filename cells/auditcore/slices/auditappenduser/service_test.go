package auditappenduser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// failingStore always fails Append with a sentinel error.
type failingStore struct {
	ledger.Store
	err error
}

func (f *failingStore) Append(_ context.Context, _ *ledger.Entry) error { return f.err }

type directRunner struct{}

var _ persistence.TxRunner = directRunner{}

func (directRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

func newTestProtocol(t testing.TB) *ledger.Protocol {
	t.Helper()
	ns, err := ledger.ParseNamespaceID("auditcore")
	require.NoError(t, err)
	p, err := ledger.NewProtocol(
		ledger.WithChainHMAC([]byte("test-hmac-key-32bytes-long!!!!!!!")),
		ledger.WithNamespace(ns),
		ledger.WithRestartRecovery(ledger.RestartRecoveryStrictTailVerify{}),
		ledger.WithIdempotency(ledger.IdempotencyContentFingerprint{}),
	)
	require.NoError(t, err)
	return p
}

func mustJSON(t testing.TB, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return b
}

func TestNewService_TxRunnerRequired(t *testing.T) {
	p := newTestProtocol(t)
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	_, err = NewService(store, p, slog.Default(), clock.Real())
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
}

func TestService_HandleEvent_UserCreated(t *testing.T) {
	p := newTestProtocol(t)
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	svc, err := NewService(store, p, slog.Default(), clock.Real(), WithTxManager(directRunner{}))
	require.NoError(t, err)

	entry := outbox.Entry{
		ID:        "evt-1",
		EventType: "event.user.created.v1",
		Payload:   mustJSON(t, map[string]any{"userId": "usr-1", "username": "alice", "actorId": "admin-1"}),
	}
	result := svc.HandleEvent(context.Background(), entry)
	assert.Equal(t, outbox.DispositionAck, result.Disposition)

	tail, err := store.Tail(context.Background())
	require.NoError(t, err)
	assert.EqualValues(t, 1, tail.EntryCount)
}

func TestService_HandleEvent_ActorMissing_Reject(t *testing.T) {
	p := newTestProtocol(t)
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	svc, err := NewService(store, p, slog.Default(), clock.Real(), WithTxManager(directRunner{}))
	require.NoError(t, err)

	entry := outbox.Entry{
		ID:        "evt-no-actor",
		EventType: "event.user.created.v1",
		Payload:   mustJSON(t, map[string]any{"username": "bob"}),
	}
	result := svc.HandleEvent(context.Background(), entry)
	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	var permErr *outbox.PermanentError
	require.ErrorAs(t, result.Err, &permErr)
}

func TestService_HandleEvent_AppendFails_Requeue(t *testing.T) {
	sentinel := fmt.Errorf("db unavailable")
	p := newTestProtocol(t)
	realStore, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	svc, err := NewService(&failingStore{Store: realStore, err: sentinel}, p, slog.Default(), clock.Real(), WithTxManager(directRunner{}))
	require.NoError(t, err)

	entry := outbox.Entry{
		ID:        "evt-fail",
		EventType: "event.user.created.v1",
		Payload:   mustJSON(t, map[string]any{"userId": "usr-1", "username": "alice", "actorId": "admin-1"}),
	}
	result := svc.HandleEvent(context.Background(), entry)
	assert.Equal(t, outbox.DispositionRequeue, result.Disposition)
	assert.ErrorIs(t, result.Err, sentinel)

	var permErr *outbox.PermanentError
	assert.False(t, errors.As(result.Err, &permErr), "transient persist error must NOT be PermanentError")
}

func TestService_HandleEvent_InvalidJSON_Reject(t *testing.T) {
	p := newTestProtocol(t)
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	svc, err := NewService(store, p, slog.Default(), clock.Real(), WithTxManager(directRunner{}))
	require.NoError(t, err)

	entry := outbox.Entry{
		ID:        "evt-bad-json",
		EventType: "event.user.created.v1",
		Payload:   []byte("{invalid json}"),
	}
	result := svc.HandleEvent(context.Background(), entry)
	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	var permErr *outbox.PermanentError
	require.ErrorAs(t, result.Err, &permErr)
}
