package sessionlogout

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
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
	repo := mem.NewSessionRepository()
	ow := &stubOutboxWriter{}
	svc := NewService(repo, eventbus.New(), slog.Default(), WithOutboxWriter(ow))

	seedSession(repo, "sess-1", "usr-1")

	require.NoError(t, svc.Logout(context.Background(), "sess-1"))

	require.Len(t, ow.entries, 1)
	assert.Equal(t, TopicSessionRevoked, ow.entries[0].EventType)
}

func TestService_WithTxManager(t *testing.T) {
	repo := mem.NewSessionRepository()
	tx := &stubTxRunner{}
	svc := NewService(repo, eventbus.New(), slog.Default(), WithTxManager(tx))

	seedSession(repo, "sess-1", "usr-1")

	require.NoError(t, svc.Logout(context.Background(), "sess-1"))
	assert.Equal(t, 1, tx.calls)
}

func TestService_WithOutboxAndTx(t *testing.T) {
	repo := mem.NewSessionRepository()
	ow := &stubOutboxWriter{}
	tx := &stubTxRunner{}
	svc := NewService(repo, eventbus.New(), slog.Default(),
		WithOutboxWriter(ow), WithTxManager(tx))

	seedSession(repo, "sess-1", "usr-1")

	require.NoError(t, svc.Logout(context.Background(), "sess-1"))
	assert.Equal(t, 1, tx.calls)
	require.Len(t, ow.entries, 1)
}

func TestService_LogoutUser_EmptyID(t *testing.T) {
	svc, _ := newTestService()
	err := svc.LogoutUser(context.Background(), "")
	assert.Error(t, err)
}
