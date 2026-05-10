package auditverify

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/auditcore/internal/dto"
	"github.com/ghbvf/gocell/cells/internal/testoutbox"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/audit/ledger"
)

// --- stubs ---

type stubOutboxWriter struct{ entries []outbox.Entry }

func (s *stubOutboxWriter) Write(_ context.Context, e outbox.Entry) error {
	s.entries = append(s.entries, e)
	return nil
}

type failingOutboxWriter struct{ err error }

func (f *failingOutboxWriter) Write(_ context.Context, _ outbox.Entry) error { return f.err }

type stubTxRunner struct{ calls int }

func (s *stubTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	s.calls++
	return fn(ctx)
}

type failingTxRunner struct{ err error }

func (f *failingTxRunner) RunInTx(_ context.Context, _ func(context.Context) error) error {
	return f.err
}

// --- helpers ---

func seedEntries(t testing.TB, store *ledger.MemStore, n int) {
	t.Helper()
	for i := range n {
		e := &ledger.Entry{
			EventID:   fmt.Sprintf("evt-%d", i),
			EventType: "event.test",
			ActorID:   "actor-1",
			Timestamp: clock.Real().Now(),
			Payload:   []byte(`{}`),
		}
		require.NoError(t, store.Append(context.Background(), e))
	}
}

// --- tests ---

func TestService_WithEmitter(t *testing.T) {
	p := newTestProtocol(t)
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	ow := &stubOutboxWriter{}
	svc, err := NewService(store, slog.Default(), WithEmitter(testoutbox.MustEmitter(t, ow)), WithTxManager(&stubTxRunner{}))
	require.NoError(t, err)

	seedEntries(t, store, 3)

	result, err := svc.VerifyChain(context.Background(), 1, 3)
	require.NoError(t, err)
	assert.True(t, result.Valid)

	require.Len(t, ow.entries, 1)
	assert.Equal(t, dto.TopicAuditIntegrityVerified, ow.entries[0].EventType)
}

func TestService_WithTxManager(t *testing.T) {
	p := newTestProtocol(t)
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	tx := &stubTxRunner{}
	_, err = NewService(store, slog.Default(), WithTxManager(tx))
	require.NoError(t, err)
	assert.Equal(t, 0, tx.calls)
}

func TestService_VerifyChain_OutboxWriteError_ReturnsError(t *testing.T) {
	p := newTestProtocol(t)
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	failErr := fmt.Errorf("outbox write failure")
	fw := &failingOutboxWriter{err: failErr}
	svc, err := NewService(store, slog.Default(), WithEmitter(testoutbox.MustEmitter(t, fw)), WithTxManager(&stubTxRunner{}))
	require.NoError(t, err)

	seedEntries(t, store, 3)

	result, verifyErr := svc.VerifyChain(context.Background(), 1, 3)
	require.Error(t, verifyErr, "outbox write error should propagate")
	assert.Contains(t, verifyErr.Error(), "outbox write failure")
	require.NotNil(t, result)
	assert.True(t, result.Valid)
}

func TestService_VerifyChain_TxRunnerError_ReturnsError(t *testing.T) {
	p := newTestProtocol(t)
	store, err := ledger.NewMemStore(p, clock.Real())
	require.NoError(t, err)
	ow := &stubOutboxWriter{}
	txErr := fmt.Errorf("db connection lost")
	ftx := &failingTxRunner{err: txErr}
	svc, err := NewService(store, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, ow)), WithTxManager(ftx))
	require.NoError(t, err)

	seedEntries(t, store, 3)

	result, verifyErr := svc.VerifyChain(context.Background(), 1, 3)
	require.Error(t, verifyErr, "txRunner error should propagate")
	assert.Contains(t, verifyErr.Error(), "db connection lost")
	require.NotNil(t, result)
	assert.True(t, result.Valid)
	assert.Empty(t, ow.entries)
}
