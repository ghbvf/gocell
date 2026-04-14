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

	// Must be PermanentError so WrapLegacyHandler routes to DLX, not retry.
	var permErr *outbox.PermanentError
	assert.ErrorAs(t, err, &permErr)
}

func TestTopicConstant(t *testing.T) {
	assert.Equal(t, "event.config.changed.v1", TopicConfigChanged)
}

// --- Behavior-level tests via WrapLegacyHandler ---
// These verify the full disposition chain: handler error → WrapLegacyHandler → HandleResult.

func TestWrapLegacyHandler_ValidPayload_Ack(t *testing.T) {
	svc := NewService(slog.Default())
	handler := outbox.WrapLegacyHandler(svc.HandleEvent)

	payload, err := json.Marshal(ConfigChangedEvent{Action: "updated", Key: "jwt.ttl"})
	require.NoError(t, err)

	entry := outbox.Entry{ID: "evt-wrap-1", Topic: TopicConfigChanged, Payload: payload}
	result := handler(context.Background(), entry)

	assert.Equal(t, outbox.DispositionAck, result.Disposition)
	assert.NoError(t, result.Err)
}

func TestWrapLegacyHandler_InvalidJSON_Reject(t *testing.T) {
	svc := NewService(slog.Default())
	handler := outbox.WrapLegacyHandler(svc.HandleEvent)

	entry := outbox.Entry{ID: "evt-wrap-2", Topic: TopicConfigChanged, Payload: []byte("bad{")}
	result := handler(context.Background(), entry)

	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	assert.Error(t, result.Err)
}

func TestWrapLegacyHandler_UnknownAction_Ack(t *testing.T) {
	svc := NewService(slog.Default())
	handler := outbox.WrapLegacyHandler(svc.HandleEvent)

	payload, err := json.Marshal(ConfigChangedEvent{Action: "unknown-future-action", Key: "x"})
	require.NoError(t, err)

	entry := outbox.Entry{ID: "evt-wrap-3", Topic: TopicConfigChanged, Payload: payload}
	result := handler(context.Background(), entry)

	assert.Equal(t, outbox.DispositionAck, result.Disposition)
	assert.NoError(t, result.Err)
}
