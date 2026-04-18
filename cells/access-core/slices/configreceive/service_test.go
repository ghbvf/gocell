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

// TestHandleEvent_UnknownAction_PermanentError verifies that an unknown action
// returns a PermanentError (fail-closed, P1-14 A3).
func TestHandleEvent_UnknownAction_PermanentError(t *testing.T) {
	svc := NewService(slog.Default())

	payload, _ := json.Marshal(map[string]string{
		"action": "bogus",
		"key":    "some.key",
	})

	entry := outbox.Entry{
		ID:      "evt-2",
		Topic:   TopicConfigChanged,
		Payload: payload,
	}

	err := svc.HandleEvent(context.Background(), entry)
	require.Error(t, err, "unknown action must return error")

	// Must be PermanentError so WrapLegacyHandler routes to DLX, not retry.
	var permErr *outbox.PermanentError
	require.ErrorAs(t, err, &permErr, "unknown action must be PermanentError")
	assert.Contains(t, err.Error(), "bogus", "error message should include the unknown action name")
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

// TestWrapLegacyHandler_UnknownAction_Reject verifies that unknown actions
// produce DispositionReject via WrapLegacyHandler (P1-14 A3).
func TestWrapLegacyHandler_UnknownAction_Reject(t *testing.T) {
	svc := NewService(slog.Default())
	handler := outbox.WrapLegacyHandler(svc.HandleEvent)

	payload, err := json.Marshal(ConfigChangedEvent{Action: "bogus-action", Key: "x"})
	require.NoError(t, err)

	entry := outbox.Entry{ID: "evt-wrap-3", Topic: TopicConfigChanged, Payload: payload}
	result := handler(context.Background(), entry)

	assert.Equal(t, outbox.DispositionReject, result.Disposition,
		"unknown action via WrapLegacyHandler must produce DispositionReject → DLX")
	assert.Error(t, result.Err)
}
