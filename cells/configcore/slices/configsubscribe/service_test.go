package configsubscribe

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeEntryUpserted(key, value string, version int) outbox.Entry {
	payload, _ := json.Marshal(domain.ConfigEntryUpsertedEvent{
		Key:     key,
		Value:   value,
		Version: version,
	})
	return outbox.Entry{ID: "test-upsert", Topic: TopicConfigEntryUpserted, Payload: payload}
}

func makeEntryDeleted(key string) outbox.Entry {
	payload, _ := json.Marshal(domain.ConfigEntryDeletedEvent{Key: key})
	return outbox.Entry{ID: "test-delete", Topic: TopicConfigEntryDeleted, Payload: payload}
}

func TestService_HandleEntryUpserted(t *testing.T) {
	tests := []struct {
		name      string
		events    []outbox.Entry
		wantKey   string
		wantValue string
		wantLen   int
	}{
		{
			name:      "created state updates cache",
			events:    []outbox.Entry{makeEntryUpserted("app.name", "gocell", 1)},
			wantKey:   "app.name",
			wantValue: "gocell",
			wantLen:   1,
		},
		{
			name: "updated state updates cache",
			events: []outbox.Entry{
				makeEntryUpserted("k", "v1", 1),
				makeEntryUpserted("k", "v2", 2),
			},
			wantKey:   "k",
			wantValue: "v2",
			wantLen:   1,
		},
		{
			name:      "empty value is cached",
			events:    []outbox.Entry{makeEntryUpserted("empty", "", 1)},
			wantKey:   "empty",
			wantValue: "",
			wantLen:   1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(slog.Default())

			for _, e := range tt.events {
				require.NoError(t, svc.HandleEntryUpserted(context.Background(), e))
			}

			assert.Equal(t, tt.wantLen, svc.Cache().Len())
			v, ok := svc.Cache().Get(tt.wantKey)
			require.True(t, ok)
			assert.Equal(t, tt.wantValue, v)
		})
	}
}

func TestService_HandleEntryDeleted(t *testing.T) {
	svc := NewService(slog.Default())
	require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", "v", 1)))
	require.NoError(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("k")))

	assert.Equal(t, 0, svc.Cache().Len())
	_, ok := svc.Cache().Get("k")
	assert.False(t, ok)
}

func TestService_HandleEntryUpserted_InvalidPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantErr string
	}{
		{"invalid json", []byte("not-json"), "unmarshal"},
		{"missing key", []byte(`{"value":"v","version":1}`), "missing key"},
		{"missing value", []byte(`{"key":"k","version":1}`), "missing value"},
		{"invalid version", []byte(`{"key":"k","value":"v","version":0}`), "invalid version"},
		{"extra sensitive field", []byte(`{"key":"k","value":"v","version":1,"sensitive":false}`), "unknown field"},
		{"old action field", []byte(`{"action":"updated","key":"k","value":"v","version":1}`), "unknown field"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(slog.Default())
			entry := outbox.Entry{ID: "bad", Topic: TopicConfigEntryUpserted, Payload: tt.payload}

			err := svc.HandleEntryUpserted(context.Background(), entry)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			assert.Equal(t, 0, svc.Cache().Len())

			var permErr *outbox.PermanentError
			require.ErrorAs(t, err, &permErr)
		})
	}
}

func TestService_HandleEntryDeleted_InvalidPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantErr string
	}{
		{"invalid json", []byte("not-json"), "unmarshal"},
		{"missing key", []byte(`{}`), "missing key"},
		{"extra value field", []byte(`{"key":"existing.key","value":"old"}`), "unknown field"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(slog.Default())
			require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("existing.key", "existing-value", 1)))

			entry := outbox.Entry{ID: "bad-delete", Topic: TopicConfigEntryDeleted, Payload: tt.payload}
			err := svc.HandleEntryDeleted(context.Background(), entry)
			require.Error(t, err)

			var permErr *outbox.PermanentError
			require.ErrorAs(t, err, &permErr)
			assert.Equal(t, 1, svc.Cache().Len(), "cache must be unchanged after invalid delete")
			v, ok := svc.Cache().Get("existing.key")
			require.True(t, ok)
			assert.Equal(t, "existing-value", v)
		})
	}
}

func TestWrapLegacyHandler_InvalidPayload_Reject(t *testing.T) {
	svc := NewService(slog.Default())
	handler := outbox.WrapLegacyHandler(svc.HandleEntryUpserted)

	entry := outbox.Entry{ID: "bad", Topic: TopicConfigEntryUpserted, Payload: []byte("not-json")}
	result := handler(context.Background(), entry)

	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	assert.Error(t, result.Err)
}

func TestWrapLegacyHandler_MissingValue_Reject(t *testing.T) {
	svc := NewService(slog.Default())
	handler := outbox.WrapLegacyHandler(svc.HandleEntryUpserted)

	entry := outbox.Entry{ID: "bad", Topic: TopicConfigEntryUpserted, Payload: []byte(`{"key":"k","version":1}`)}
	result := handler(context.Background(), entry)

	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	assert.Error(t, result.Err)
}
