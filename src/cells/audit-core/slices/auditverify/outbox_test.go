package auditverify

import (
	"context"
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

type stubTxRunner struct{ calls int }

func (s *stubTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	s.calls++
	return fn(context.Background())
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
		_ = repo.Append(context.Background(), entry)
	}

	result, err := svc.VerifyChain(context.Background(), 0, 10)
	require.NoError(t, err)
	assert.False(t, result.Valid)

	// Even on invalid chain, the event is published.
	require.Len(t, ow.entries, 1)
}
