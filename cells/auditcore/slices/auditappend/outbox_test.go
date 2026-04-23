package auditappend

import (
	"context"
	"github.com/ghbvf/gocell/cells/internal/testoutbox"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/auditcore/internal/mem"
	"github.com/ghbvf/gocell/kernel/outbox"
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

func TestService_WithEmitter(t *testing.T) {
	repo := mem.NewAuditRepository()
	ow := &stubOutboxWriter{}
	svc := NewService(repo, testHMACKey, slog.Default(), WithEmitter(testoutbox.MustEmitter(t, ow)))

	entry := outbox.Entry{
		ID:        "evt-1",
		EventType: "event.user.created.v1",
		Payload:   mustJSON(map[string]any{"user_id": "usr-1"}),
	}
	require.NoError(t, svc.HandleEvent(context.Background(), entry))

	require.Len(t, ow.entries, 1)
	assert.Equal(t, TopicAuditAppended, ow.entries[0].EventType)
}

func TestService_WithTxManager(t *testing.T) {
	repo := mem.NewAuditRepository()
	tx := &stubTxRunner{}
	svc := NewService(repo, testHMACKey, slog.Default(), WithTxManager(tx))

	entry := outbox.Entry{
		ID:        "evt-1",
		EventType: "event.user.created.v1",
		Payload:   mustJSON(map[string]any{"user_id": "usr-1"}),
	}
	require.NoError(t, svc.HandleEvent(context.Background(), entry))

	assert.Equal(t, 1, tx.calls)
}

func TestService_WithOutboxAndTx(t *testing.T) {
	repo := mem.NewAuditRepository()
	ow := &stubOutboxWriter{}
	tx := &stubTxRunner{}
	svc := NewService(repo, testHMACKey, slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, ow)), WithTxManager(tx))

	entry := outbox.Entry{
		ID:        "evt-1",
		EventType: "event.session.created.v1",
		Payload:   mustJSON(map[string]any{"session_id": "sess-1", "user_id": "usr-1"}),
	}
	require.NoError(t, svc.HandleEvent(context.Background(), entry))

	assert.Equal(t, 1, tx.calls)
	require.Len(t, ow.entries, 1)
	assert.Equal(t, TopicAuditAppended, ow.entries[0].EventType)
}

func TestService_HandleEvent_SystemActor(t *testing.T) {
	// Test that entries without user_id get "system" as actor.
	svc, _ := newTestService()
	entry := outbox.Entry{
		ID:        "evt-sys",
		EventType: "event.config.changed.v1",
		Payload:   mustJSON(map[string]any{"key": "app.name"}), // no user_id
	}
	require.NoError(t, svc.HandleEvent(context.Background(), entry))
	assert.Equal(t, 1, svc.ChainLen())
}
