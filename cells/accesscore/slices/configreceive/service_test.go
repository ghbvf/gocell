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

func stringPtr(v string) *string {
	return &v
}

func TestHandleEntryUpserted_ValidPayload(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"non-empty value", "30m"},
		{"empty value is present", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(slog.Default())
			payload, err := json.Marshal(ConfigEntryUpsertedEvent{
				Key:     "jwt.ttl",
				Value:   stringPtr(tt.value),
				Version: 1,
			})
			require.NoError(t, err)

			entry := outbox.Entry{
				ID:      "evt-1",
				Topic:   TopicConfigEntryUpserted,
				Payload: payload,
			}

			assert.NoError(t, svc.HandleEntryUpserted(context.Background(), entry))
		})
	}
}

func TestHandleEntryUpserted_InvalidPayload_PermanentError(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantErr string
	}{
		{"invalid json", []byte("not-json{"), "unmarshal"},
		{"missing key", []byte(`{"value":"30m","version":1}`), "missing key"},
		{"missing value field", []byte(`{"key":"jwt.ttl","version":1}`), "missing value"},
		{"invalid version", []byte(`{"key":"jwt.ttl","value":"30m","version":0}`), "invalid version"},
		{"extra sensitive field", []byte(`{"key":"jwt.ttl","value":"30m","version":1,"sensitive":false}`), "unknown field"},
		{"old action field", []byte(`{"action":"updated","key":"jwt.ttl","value":"30m","version":1}`), "unknown field"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(slog.Default())
			entry := outbox.Entry{
				ID:      "evt-bad",
				Topic:   TopicConfigEntryUpserted,
				Payload: tt.payload,
			}

			err := svc.HandleEntryUpserted(context.Background(), entry)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)

			var permErr *outbox.PermanentError
			assert.ErrorAs(t, err, &permErr)
		})
	}
}

func TestHandleEntryDeleted_ValidPayload(t *testing.T) {
	svc := NewService(slog.Default())
	payload, err := json.Marshal(ConfigEntryDeletedEvent{Key: "jwt.ttl"})
	require.NoError(t, err)

	entry := outbox.Entry{
		ID:      "evt-del-1",
		Topic:   TopicConfigEntryDeleted,
		Payload: payload,
	}

	assert.NoError(t, svc.HandleEntryDeleted(context.Background(), entry))
}

func TestHandleEntryDeleted_InvalidPayload_PermanentError(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantErr string
	}{
		{"invalid json", []byte("not-json{"), "unmarshal"},
		{"missing key", []byte(`{}`), "missing key"},
		{"extra value field", []byte(`{"key":"jwt.ttl","value":"old"}`), "unknown field"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(slog.Default())
			entry := outbox.Entry{
				ID:      "evt-del-bad",
				Topic:   TopicConfigEntryDeleted,
				Payload: tt.payload,
			}

			err := svc.HandleEntryDeleted(context.Background(), entry)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)

			var permErr *outbox.PermanentError
			assert.ErrorAs(t, err, &permErr)
		})
	}
}

func TestTopicConstants(t *testing.T) {
	assert.Equal(t, "event.config.entry-upserted.v1", TopicConfigEntryUpserted)
	assert.Equal(t, "event.config.entry-deleted.v1", TopicConfigEntryDeleted)
}

func TestWrapLegacyHandler_EntryUpserted_ValidPayload_Ack(t *testing.T) {
	svc := NewService(slog.Default())
	handler := outbox.WrapLegacyHandler(svc.HandleEntryUpserted)

	payload, err := json.Marshal(ConfigEntryUpsertedEvent{
		Key:     "jwt.ttl",
		Value:   stringPtr(""),
		Version: 1,
	})
	require.NoError(t, err)

	entry := outbox.Entry{ID: "evt-wrap-1", Topic: TopicConfigEntryUpserted, Payload: payload}
	result := handler(context.Background(), entry)

	assert.Equal(t, outbox.DispositionAck, result.Disposition)
	assert.NoError(t, result.Err)
}

func TestWrapLegacyHandler_EntryUpserted_InvalidJSON_Reject(t *testing.T) {
	svc := NewService(slog.Default())
	handler := outbox.WrapLegacyHandler(svc.HandleEntryUpserted)

	entry := outbox.Entry{ID: "evt-wrap-2", Topic: TopicConfigEntryUpserted, Payload: []byte("bad{")}
	result := handler(context.Background(), entry)

	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	assert.Error(t, result.Err)
}

func TestWrapLegacyHandler_EntryUpserted_MissingValue_Reject(t *testing.T) {
	svc := NewService(slog.Default())
	handler := outbox.WrapLegacyHandler(svc.HandleEntryUpserted)

	entry := outbox.Entry{
		ID:      "evt-wrap-3",
		Topic:   TopicConfigEntryUpserted,
		Payload: []byte(`{"key":"jwt.ttl","version":1}`),
	}
	result := handler(context.Background(), entry)

	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	assert.Error(t, result.Err)
}
