package auditappend

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/auditcore/internal/mem"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
)

var testHMACKey = []byte("test-hmac-key-32bytes-long!!!!!!!")

func newTestService() (*Service, *mem.AuditRepository) {
	repo := mem.NewAuditRepository()
	return NewService(repo, testHMACKey, slog.Default()), repo
}

func TestService_HandleEvent(t *testing.T) {
	tests := []struct {
		name      string
		entry     outbox.Entry
		wantErr   bool
		wantChain int
	}{
		{
			name: "user created event",
			entry: outbox.Entry{
				ID:        "evt-1",
				EventType: "event.user.created.v1",
				Payload:   mustJSON(map[string]any{"userId": "usr-1"}),
			},
			wantChain: 1,
		},
		{
			name: "session created event",
			entry: outbox.Entry{
				ID:        "evt-2",
				EventType: "event.session.created.v1",
				Payload:   mustJSON(map[string]any{"sessionId": "sess-1", "userId": "usr-1"}),
			},
			wantChain: 1,
		},
		{
			name: "config entry-upserted event (no userId → system actor)",
			entry: outbox.Entry{
				ID:        "evt-3",
				EventType: "event.config.entry-upserted.v1",
				Payload:   mustJSON(map[string]any{"key": "app.name", "value": "v", "version": 1}),
			},
			wantChain: 1,
		},
		{
			name: "user created event with snake_case user_id (transitional)",
			entry: outbox.Entry{
				ID:        "evt-4",
				EventType: "event.user.created.v1",
				Payload:   mustJSON(map[string]any{"user_id": "usr-2", "username": "bob"}),
			},
			wantChain: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTestService()

			err := svc.HandleEvent(context.Background(), tt.entry)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.wantChain, svc.ChainLen())
				assert.Equal(t, tt.wantChain, repo.Len())
			}
		})
	}
}

func TestService_HandleEvent_ChainGrows(t *testing.T) {
	svc, repo := newTestService()

	for i := range 5 {
		entry := outbox.Entry{
			ID:        "evt-" + string(rune('0'+i)),
			EventType: "event.user.created.v1",
			Payload:   mustJSON(map[string]any{"userId": "usr-1"}),
		}
		require.NoError(t, svc.HandleEvent(context.Background(), entry))
	}

	assert.Equal(t, 5, svc.ChainLen())
	assert.Equal(t, 5, repo.Len())
}

// TestService_HandleEvent_InvalidPayload_PermanentError asserts that an invalid
// JSON payload causes HandleEvent to return a PermanentError, routing the event
// to the DLX instead of silently appending it with a fallback "system" actor.
func TestService_HandleEvent_InvalidPayload_PermanentError(t *testing.T) {
	repo := mem.NewAuditRepository()
	svc := NewService(repo, testHMACKey, slog.Default())

	entry := outbox.Entry{
		ID:        "evt-bad-json",
		EventType: "event.user.created.v1",
		Payload:   []byte("{invalid json}"),
	}

	err := svc.HandleEvent(context.Background(), entry)
	require.Error(t, err, "invalid JSON payload must cause HandleEvent to fail (route to DLX)")

	var permErr *outbox.PermanentError
	require.ErrorAs(t, err, &permErr, "error must be a PermanentError so legacy handler wrapper routes to DLX")
	assert.Contains(t, err.Error(), "evt-bad-json", "error message must contain event ID")

	// Verify the chain was NOT appended — invalid payload must not pollute the audit chain.
	assert.Equal(t, 0, svc.ChainLen(), "invalid payload must not be appended to the audit chain")
	assert.Equal(t, 0, repo.Len(), "invalid payload must not be persisted")
}

type failingPublisher struct{ err error }

func (f failingPublisher) Publish(_ context.Context, _ string, _ []byte) error { return f.err }
func (f failingPublisher) Close(_ context.Context) error                       { return nil }

func TestService_HandleEvent_PublishError_DoesNotFailAppend(t *testing.T) {
	repo := mem.NewAuditRepository()
	fp := failingPublisher{err: fmt.Errorf("broker unavailable")}
	emitter, err := outbox.NewDirectEmitter(
		fp, outbox.DirectPublishFailOpen, metrics.NopProvider{}, clock.Real(), "auditcore",
		outbox.WithLogger(slog.Default()))
	require.NoError(t, err)
	svc := NewService(repo, testHMACKey, slog.Default(), WithEmitter(emitter))

	entry := outbox.Entry{
		ID:        "evt-pub-err",
		EventType: "event.user.created.v1",
		Payload:   mustJSON(map[string]any{"userId": "usr-1"}),
	}
	err = svc.HandleEvent(context.Background(), entry)
	require.NoError(t, err, "publish failure in demo mode should not fail append")
	assert.Equal(t, 1, svc.ChainLen(), "entry should still be appended to chain")
}

// TestService_HandleEvent_ActorExtraction covers the actor-id extractor's
// priority: actorId (preferred) > userId (fallback) > "system" (default).
// G.6 migrated user events from snake_case user_id to camelCase userId; G.2
// added required actorId to all admin-write events.
func TestService_HandleEvent_ActorExtraction(t *testing.T) {
	tests := []struct {
		name        string
		eventType   string
		payload     map[string]any
		wantActorID string
	}{
		{
			name:        "camelCase userId (session.created)",
			eventType:   "event.session.created.v1",
			payload:     map[string]any{"sessionId": "sess-1", "userId": "usr-cam"},
			wantActorID: "usr-cam",
		},
		{
			name:        "actorId field (user.locked, PR-CFG-G1)",
			eventType:   "event.user.locked.v1",
			payload:     map[string]any{"actorId": "adm-1", "userId": "usr-snk"},
			wantActorID: "adm-1",
		},
		{
			name:        "actorId field (config.entry-upserted, PR-CFG-G1) — production path",
			eventType:   "event.config.entry-upserted.v1",
			payload:     map[string]any{"key": "k", "version": 1, "actorId": "adm-99"},
			wantActorID: "adm-99",
		},
		{
			name:        "no actor field (legacy config event) → system fallback",
			eventType:   "event.config.entry-upserted.v1",
			payload:     map[string]any{"key": "k", "version": 1},
			wantActorID: "system",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, repo := newTestService()
			entry := outbox.Entry{
				ID:        "evt-" + tt.name,
				EventType: tt.eventType,
				Payload:   mustJSON(tt.payload),
			}
			require.NoError(t, svc.HandleEvent(context.Background(), entry))

			entries, err := repo.GetRange(context.Background(), 0, 1)
			require.NoError(t, err)
			require.Len(t, entries, 1)
			assert.Equal(t, tt.wantActorID, entries[0].ActorID)
		})
	}
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
