package sessionlogout

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/cells/internal/testoutbox"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
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
	store := testutil.RealSessionRepo(t)
	ow := &stubOutboxWriter{}
	svc := mustNewService(store, newLogoutRefreshStore(), slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, ow)),
		WithTxManager(persistence.WrapForCell(noopTxRunner{})))

	seedSession(store, "sess-1", "usr-1")

	require.NoError(t, svc.Logout(context.Background(), "sess-1", "usr-1"))

	require.Len(t, ow.entries, 1)
	assert.Equal(t, dto.TopicSessionRevoked, ow.entries[0].EventType)
}

func TestService_WithTxManager(t *testing.T) {
	store := testutil.RealSessionRepo(t)
	tx := &stubTxRunner{}
	svc := mustNewService(store, newLogoutRefreshStore(), slog.Default(), WithTxManager(persistence.WrapForCell(tx)))

	seedSession(store, "sess-1", "usr-1")

	require.NoError(t, svc.Logout(context.Background(), "sess-1", "usr-1"))
	assert.Equal(t, 1, tx.calls)
}

func TestService_WithOutboxAndTx(t *testing.T) {
	store := testutil.RealSessionRepo(t)
	ow := &stubOutboxWriter{}
	tx := &stubTxRunner{}
	svc := mustNewService(store, newLogoutRefreshStore(), slog.Default(),
		WithEmitter(testoutbox.MustEmitter(t, ow)), WithTxManager(persistence.WrapForCell(tx)))

	seedSession(store, "sess-1", "usr-1")

	require.NoError(t, svc.Logout(context.Background(), "sess-1", "usr-1"))
	assert.Equal(t, 1, tx.calls)
	require.Len(t, ow.entries, 1)
}
