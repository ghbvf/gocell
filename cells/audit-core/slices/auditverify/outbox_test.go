package auditverify

import (
	"context"
	"fmt"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/audit-core/internal/domain"
	"github.com/ghbvf/gocell/cells/audit-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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

func (s *stubTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	s.calls++
	return fn(context.Background())
}

type failingTxRunner struct{ err error }

func (f *failingTxRunner) RunInTx(_ context.Context, _ func(context.Context) error) error {
	return f.err
}

// --- tests ---

func TestService_WithOutboxWriter(t *testing.T) {
	repo := mem.NewAuditRepository()
	ow := &stubOutboxWriter{}
	svc := NewService(repo, testHMACKey, eventbus.New(), slog.Default(), WithOutboxWriter(ow))

	// Build a small valid chain.
	chain := domain.NewHashChain(testHMACKey)
	for i := range 3 {
		entry := chain.Append("evt-"+string(rune('0'+i)), "event.test", "actor-1", []byte("payload"))
		require.NoError(t, repo.Append(context.Background(), entry))
	}

	result, err := svc.VerifyChain(context.Background(), 0, 10)
	require.NoError(t, err)
	assert.True(t, result.Valid)
	assert.Equal(t, 3, result.EntriesChecked)

	// Outbox should have received the integrity-verified event.
	require.Len(t, ow.entries, 1)
	assert.Equal(t, TopicIntegrityVerified, ow.entries[0].EventType)
}

func TestService_WithTxManager(t *testing.T) {
	repo := mem.NewAuditRepository()
	tx := &stubTxRunner{}
	_ = NewService(repo, testHMACKey, eventbus.New(), slog.Default(), WithTxManager(tx))
	// TxManager option is set — verifying it compiles and runs.
	assert.Equal(t, 0, tx.calls)
}

func TestService_VerifyChain_OutboxWriteError_ReturnsError(t *testing.T) {
	repo := mem.NewAuditRepository()
	failErr := fmt.Errorf("outbox write failure")
	fw := &failingOutboxWriter{err: failErr}
	svc := NewService(repo, testHMACKey, eventbus.New(), slog.Default(), WithOutboxWriter(fw))

	// Build a valid chain so we reach the outbox write path.
	chain := domain.NewHashChain(testHMACKey)
	for i := range 3 {
		entry := chain.Append("evt-"+string(rune('0'+i)), "event.test", "actor-1", []byte("payload"))
		require.NoError(t, repo.Append(context.Background(), entry))
	}

	result, err := svc.VerifyChain(context.Background(), 0, 10)
	// Durable path: outbox write error must propagate, not be swallowed.
	require.Error(t, err, "outbox write error should propagate in durable mode")
	assert.Contains(t, err.Error(), "outbox write failure")
	// Result should still be returned (verification completed before persist).
	require.NotNil(t, result)
	assert.True(t, result.Valid)
}

func TestService_VerifyChain_WithTxRunner_RunsInTx(t *testing.T) {
	repo := mem.NewAuditRepository()
	ow := &stubOutboxWriter{}
	tx := &stubTxRunner{}
	svc := NewService(repo, testHMACKey, eventbus.New(), slog.Default(),
		WithOutboxWriter(ow), WithTxManager(tx))

	chain := domain.NewHashChain(testHMACKey)
	for i := range 3 {
		entry := chain.Append("evt-"+string(rune('0'+i)), "event.test", "actor-1", []byte("payload"))
		require.NoError(t, repo.Append(context.Background(), entry))
	}

	result, err := svc.VerifyChain(context.Background(), 0, 10)
	require.NoError(t, err)
	assert.True(t, result.Valid)
	assert.Equal(t, 1, tx.calls, "txRunner should have been called once")
	require.Len(t, ow.entries, 1, "outbox writer should have received the event within tx")
}

func TestService_VerifyChain_TxRunnerError_ReturnsError(t *testing.T) {
	repo := mem.NewAuditRepository()
	ow := &stubOutboxWriter{}
	txErr := fmt.Errorf("db connection lost")
	ftx := &failingTxRunner{err: txErr}
	svc := NewService(repo, testHMACKey, eventbus.New(), slog.Default(),
		WithOutboxWriter(ow), WithTxManager(ftx))

	chain := domain.NewHashChain(testHMACKey)
	for i := range 3 {
		entry := chain.Append("evt-"+string(rune('0'+i)), "event.test", "actor-1", []byte("payload"))
		require.NoError(t, repo.Append(context.Background(), entry))
	}

	result, err := svc.VerifyChain(context.Background(), 0, 10)
	// TxRunner error must propagate — fn is never called.
	require.Error(t, err, "txRunner error should propagate")
	assert.Contains(t, err.Error(), "db connection lost")
	require.NotNil(t, result, "result should still be returned")
	assert.True(t, result.Valid, "verification completed before persist")
	assert.Empty(t, ow.entries, "outbox writer should not be called when txRunner fails")
}

type failingPublisher struct{ err error }

func (f failingPublisher) Publish(_ context.Context, _ string, _ []byte) error { return f.err }

func TestService_VerifyChain_PublishError_DoesNotFailVerify(t *testing.T) {
	repo := mem.NewAuditRepository()
	fp := failingPublisher{err: fmt.Errorf("broker unavailable")}
	// No outboxWriter → goes through direct-publish path.
	svc := NewService(repo, testHMACKey, fp, slog.Default())

	chain := domain.NewHashChain(testHMACKey)
	for i := range 3 {
		entry := chain.Append("evt-"+string(rune('0'+i)), "event.test", "actor-1", []byte("payload"))
		require.NoError(t, repo.Append(context.Background(), entry))
	}

	result, err := svc.VerifyChain(context.Background(), 0, 10)
	require.NoError(t, err, "publish failure in demo mode should not fail verify")
	assert.True(t, result.Valid)
	assert.Equal(t, 3, result.EntriesChecked)
}

func TestService_VerifyChain_InvalidChain_WithOutbox(t *testing.T) {
	repo := mem.NewAuditRepository()
	ow := &stubOutboxWriter{}
	svc := NewService(repo, testHMACKey, eventbus.New(), slog.Default(), WithOutboxWriter(ow))

	chain := domain.NewHashChain(testHMACKey)
	for i := range 3 {
		entry := chain.Append("evt-"+string(rune('0'+i)), "event.test", "actor-1", []byte("payload"))
		if i == 1 {
			entry.Hash = "tampered"
		}
		require.NoError(t, repo.Append(context.Background(), entry))
	}

	result, err := svc.VerifyChain(context.Background(), 0, 10)
	require.NoError(t, err)
	assert.False(t, result.Valid)

	// Even on invalid chain, the event is published.
	require.Len(t, ow.entries, 1)
}
