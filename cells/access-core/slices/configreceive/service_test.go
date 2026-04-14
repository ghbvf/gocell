package configreceive

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHandleEvent_ValidPayload(t *testing.T) {
	tests := []struct {
		name   string
		action string
	}{
		{"created", "created"},
		{"updated", "updated"},
		{"deleted", "deleted"},
		{"published", "published"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(slog.Default())

			payload, err := json.Marshal(map[string]string{
				"action": tt.action,
				"key":    "jwt.ttl",
				"value":  "30m",
			})
			require.NoError(t, err)

			entry := outbox.Entry{
				ID:      "evt-1",
				Topic:   TopicConfigChanged,
				Payload: payload,
			}

			err = svc.HandleEvent(context.Background(), entry)
			assert.NoError(t, err)
		})
	}
}

func TestHandleEvent_UnknownAction(t *testing.T) {
	svc := NewService(slog.Default())

	payload, _ := json.Marshal(map[string]string{
		"action": "unknown-action",
		"key":    "some.key",
	})

	entry := outbox.Entry{
		ID:      "evt-2",
		Topic:   TopicConfigChanged,
		Payload: payload,
	}

	// Unknown action is logged but not an error (no side effects to fail).
	err := svc.HandleEvent(context.Background(), entry)
	assert.NoError(t, err)
}

func TestHandleEvent_InvalidJSON(t *testing.T) {
	svc := NewService(slog.Default())

	entry := outbox.Entry{
		ID:      "evt-3",
		Topic:   TopicConfigChanged,
		Payload: []byte("not-json{"),
	}

	// Invalid JSON is a permanent error — should be rejected (not retried).
	err := svc.HandleEvent(context.Background(), entry)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")
}

func TestTopicConstant(t *testing.T) {
	assert.Equal(t, "event.config.changed.v1", TopicConfigChanged)
}
