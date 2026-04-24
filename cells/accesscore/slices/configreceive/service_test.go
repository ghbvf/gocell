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

func TestHandleEntryWritten_ValidPayload(t *testing.T) {
	tests := []struct {
		name   string
		action ConfigEntryWrittenAction
	}{
		{"created", configEntryActionCreated},
		{"updated", configEntryActionUpdated},
		{"deleted", configEntryActionDeleted},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(slog.Default())

			payload, err := json.Marshal(ConfigEntryWrittenEvent{
				Action: tt.action,
				Key:    "jwt.ttl",
				Value:  "30m",
			})
			require.NoError(t, err)

			entry := outbox.Entry{
				ID:      "evt-1",
				Topic:   TopicConfigEntryWritten,
				Payload: payload,
			}

			err = svc.HandleEntryWritten(context.Background(), entry)
			assert.NoError(t, err)
		})
	}
}

// TestHandleEntryWritten_UnknownAction_PermanentError verifies that an unknown action
// returns a PermanentError (fail-closed, P1-14 A3).
func TestHandleEntryWritten_UnknownAction_PermanentError(t *testing.T) {
	svc := NewService(slog.Default())

	payload, _ := json.Marshal(ConfigEntryWrittenEvent{
		Action: "bogus",
		Key:    "some.key",
	})

	entry := outbox.Entry{
		ID:      "evt-2",
		Topic:   TopicConfigEntryWritten,
		Payload: payload,
	}

	err := svc.HandleEntryWritten(context.Background(), entry)
	require.Error(t, err, "unknown action must return error")

	// Must be PermanentError so WrapLegacyHandler routes to DLX, not retry.
	var permErr *outbox.PermanentError
	require.ErrorAs(t, err, &permErr, "unknown action must be PermanentError")
	assert.Contains(t, err.Error(), "bogus", "error message should include the unknown action name")
}

func TestHandleEntryWritten_InvalidJSON(t *testing.T) {
	svc := NewService(slog.Default())

	entry := outbox.Entry{
		ID:      "evt-3",
		Topic:   TopicConfigEntryWritten,
		Payload: []byte("not-json{"),
	}

	// Invalid JSON is a permanent error — should be rejected (not retried).
	err := svc.HandleEntryWritten(context.Background(), entry)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "unmarshal")

	// Must be PermanentError so WrapLegacyHandler routes to DLX, not retry.
	var permErr *outbox.PermanentError
	assert.ErrorAs(t, err, &permErr)
}

func TestHandleVersionPublished_ValidPayload(t *testing.T) {
	svc := NewService(slog.Default())

	payload, err := json.Marshal(ConfigVersionPublishedEvent{
		Key:      "jwt.ttl",
		ConfigID: "cfg-xyz",
		Version:  3,
	})
	require.NoError(t, err)

	entry := outbox.Entry{
		ID:      "evt-vp-1",
		Topic:   TopicConfigVersionPublished,
		Payload: payload,
	}

	require.NoError(t, svc.HandleVersionPublished(context.Background(), entry))
}

func TestHandleVersionPublished_InvalidJSON(t *testing.T) {
	svc := NewService(slog.Default())

	entry := outbox.Entry{
		ID:      "evt-vp-2",
		Topic:   TopicConfigVersionPublished,
		Payload: []byte("not-json{"),
	}

	err := svc.HandleVersionPublished(context.Background(), entry)
	require.Error(t, err)

	var permErr *outbox.PermanentError
	assert.ErrorAs(t, err, &permErr)
}

func TestTopicConstants(t *testing.T) {
	assert.Equal(t, "event.config.entry-written.v1", TopicConfigEntryWritten)
	assert.Equal(t, "event.config.version-published.v1", TopicConfigVersionPublished)
}

// --- Behavior-level tests via WrapLegacyHandler ---
// These verify the full disposition chain: handler error → WrapLegacyHandler → HandleResult.

func TestWrapLegacyHandler_EntryWritten_ValidPayload_Ack(t *testing.T) {
	svc := NewService(slog.Default())
	handler := outbox.WrapLegacyHandler(svc.HandleEntryWritten)

	payload, err := json.Marshal(ConfigEntryWrittenEvent{Action: "updated", Key: "jwt.ttl"})
	require.NoError(t, err)

	entry := outbox.Entry{ID: "evt-wrap-1", Topic: TopicConfigEntryWritten, Payload: payload}
	result := handler(context.Background(), entry)

	assert.Equal(t, outbox.DispositionAck, result.Disposition)
	assert.NoError(t, result.Err)
}

func TestWrapLegacyHandler_EntryWritten_InvalidJSON_Reject(t *testing.T) {
	svc := NewService(slog.Default())
	handler := outbox.WrapLegacyHandler(svc.HandleEntryWritten)

	entry := outbox.Entry{ID: "evt-wrap-2", Topic: TopicConfigEntryWritten, Payload: []byte("bad{")}
	result := handler(context.Background(), entry)

	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	assert.Error(t, result.Err)
}

// TestWrapLegacyHandler_EntryWritten_UnknownAction_Reject verifies that unknown actions
// produce DispositionReject via WrapLegacyHandler (P1-14 A3).
func TestWrapLegacyHandler_EntryWritten_UnknownAction_Reject(t *testing.T) {
	svc := NewService(slog.Default())
	handler := outbox.WrapLegacyHandler(svc.HandleEntryWritten)

	payload, err := json.Marshal(ConfigEntryWrittenEvent{Action: "bogus-action", Key: "x"})
	require.NoError(t, err)

	entry := outbox.Entry{ID: "evt-wrap-3", Topic: TopicConfigEntryWritten, Payload: payload}
	result := handler(context.Background(), entry)

	assert.Equal(t, outbox.DispositionReject, result.Disposition,
		"unknown action via WrapLegacyHandler must produce DispositionReject → DLX")
	assert.Error(t, result.Err)
}
