package auditappend

import (
	"context"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/auditcore/internal/dto"
	"github.com/ghbvf/gocell/cells/auditcore/internal/mem"
	"github.com/ghbvf/gocell/cells/internal/testoutbox"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
)

// --- stubs ---

type stubOutboxWriter struct{ entries []outbox.Entry }

func (s *stubOutboxWriter) Write(_ context.Context, e outbox.Entry) error {
	s.entries = append(s.entries, e)
	return nil
}

type stubTxRunner struct{ calls int }

func (s *stubTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	s.calls++
	return fn(ctx)
}

// --- tests ---

func TestService_WithEmitter(t *testing.T) {
	repo := mem.NewAuditRepository()
	ow := &stubOutboxWriter{}
	svc, err := NewService(repo, testHMACKey, slog.Default(), clock.Real(),
		WithClock(clock.Real()),
		WithEmitter(testoutbox.MustEmitter(t, ow)),
		WithTxManager(directRunner{}))
	require.NoError(t, err)

	entry := outbox.Entry{
		ID:        "evt-1",
		EventType: "event.user.created.v1",
		Payload:   mustJSON(map[string]any{"userId": "usr-1"}),
	}
	assertAck(t, svc.HandleEvent(context.Background(), entry))

	require.Len(t, ow.entries, 1)
	assert.Equal(t, dto.TopicAuditAppended, ow.entries[0].EventType)
}

func TestService_WithTxManager(t *testing.T) {
	repo := mem.NewAuditRepository()
	tx := &stubTxRunner{}
	svc, err := NewService(repo, testHMACKey, slog.Default(), clock.Real(), WithClock(clock.Real()), WithTxManager(tx))
	require.NoError(t, err)

	entry := outbox.Entry{
		ID:        "evt-1",
		EventType: "event.user.created.v1",
		Payload:   mustJSON(map[string]any{"userId": "usr-1"}),
	}
	assertAck(t, svc.HandleEvent(context.Background(), entry))

	assert.Equal(t, 1, tx.calls)
}

func TestService_WithOutboxAndTx(t *testing.T) {
	repo := mem.NewAuditRepository()
	ow := &stubOutboxWriter{}
	tx := &stubTxRunner{}
	svc, err := NewService(repo, testHMACKey, slog.Default(), clock.Real(),
		WithClock(clock.Real()), WithEmitter(testoutbox.MustEmitter(t, ow)), WithTxManager(tx))
	require.NoError(t, err)

	entry := outbox.Entry{
		ID:        "evt-1",
		EventType: "event.session.created.v1",
		Payload:   mustJSON(map[string]any{"sessionId": "sess-1", "userId": "usr-1"}),
	}
	assertAck(t, svc.HandleEvent(context.Background(), entry))

	assert.Equal(t, 1, tx.calls)
	require.Len(t, ow.entries, 1)
	assert.Equal(t, dto.TopicAuditAppended, ow.entries[0].EventType)
}

func TestService_HandleEvent_SystemActor(t *testing.T) {
	// Test that entries without userId get "system" as actor.
	svc, _ := newTestService(t)
	entry := outbox.Entry{
		ID:        "evt-sys",
		EventType: "event.config.entry-upserted.v1",
		Payload:   mustJSON(map[string]any{"key": "app.name"}), // no userId
	}
	assertAck(t, svc.HandleEvent(context.Background(), entry))
	assert.Equal(t, 1, svc.ChainLen())
}
