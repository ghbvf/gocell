package auditappend

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/audit-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testHMACKey = []byte("test-hmac-key-32bytes-long!!!!!!!")

func newTestService() (*Service, *mem.AuditRepository) {
	repo := mem.NewAuditRepository()
	eb := eventbus.New()
	return NewService(repo, testHMACKey, eb, slog.Default()), repo
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
				Payload:   mustJSON(map[string]any{"user_id": "usr-1"}),
			},
			wantChain: 1,
		},
		{
			name: "session created event",
			entry: outbox.Entry{
				ID:        "evt-2",
				EventType: "event.session.created.v1",
				Payload:   mustJSON(map[string]any{"session_id": "sess-1", "user_id": "usr-1"}),
			},
			wantChain: 1,
		},
		{
			name: "empty payload",
			entry: outbox.Entry{
				ID:        "evt-3",
				EventType: "event.config.changed.v1",
				Payload:   []byte("{}"),
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
			Payload:   mustJSON(map[string]any{"user_id": "usr-1"}),
		}
		require.NoError(t, svc.HandleEvent(context.Background(), entry))
	}

	assert.Equal(t, 5, svc.ChainLen())
	assert.Equal(t, 5, repo.Len())
}

func TestService_HandleEvent_InvalidPayload_LogsWarning(t *testing.T) {
	repo := mem.NewAuditRepository()
	eb := eventbus.New()
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelWarn}))
	svc := NewService(repo, testHMACKey, eb, logger)

	entry := outbox.Entry{
		ID:        "evt-bad-json",
		EventType: "event.user.created.v1",
		Payload:   []byte("{invalid json}"),
	}

	err := svc.HandleEvent(context.Background(), entry)
	require.NoError(t, err, "invalid payload should not cause HandleEvent to fail")

	// Verify audit entry was created with fallback actorID.
	entries, getErr := repo.GetRange(context.Background(), 0, 1)
	require.NoError(t, getErr)
	require.Len(t, entries, 1)
	assert.Equal(t, "system", entries[0].ActorID, "should fallback to system actor")

	// Verify warning was logged.
	logOutput := logBuf.String()
	assert.Contains(t, logOutput, "failed to extract actor from payload")
	assert.Contains(t, logOutput, "evt-bad-json")
}

type failingPublisher struct{ err error }

func (f failingPublisher) Publish(_ context.Context, _ string, _ []byte) error { return f.err }
func (f failingPublisher) Close(_ context.Context) error                       { return nil }

func TestService_HandleEvent_PublishError_DoesNotFailAppend(t *testing.T) {
	repo := mem.NewAuditRepository()
	fp := failingPublisher{err: fmt.Errorf("broker unavailable")}
	svc := NewService(repo, testHMACKey, fp, slog.Default())

	entry := outbox.Entry{
		ID:        "evt-pub-err",
		EventType: "event.user.created.v1",
		Payload:   mustJSON(map[string]any{"user_id": "usr-1"}),
	}
	err := svc.HandleEvent(context.Background(), entry)
	require.NoError(t, err, "publish failure in demo mode should not fail append")
	assert.Equal(t, 1, svc.ChainLen(), "entry should still be appended to chain")
}

func mustJSON(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}
