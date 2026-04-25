package configsubscribe

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/configcore/internal/domain"
	configevents "github.com/ghbvf/gocell/cells/configcore/internal/events"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeEntryUpserted builds a metadata-only outbox.Entry for entry-upserted.
// The payload carries only key+version — no value field.
func makeEntryUpserted(key string, version int) outbox.Entry {
	payload, _ := json.Marshal(configevents.EntryUpserted{
		Key:     key,
		Version: version,
	})
	return outbox.Entry{ID: "test-upsert", Topic: domain.TopicConfigEntryUpserted, Payload: payload}
}

func makeEntryDeleted(key string) outbox.Entry {
	payload, _ := json.Marshal(configevents.EntryDeleted{Key: key})
	return outbox.Entry{ID: "test-delete", Topic: domain.TopicConfigEntryDeleted, Payload: payload}
}

func TestService_HandleEntryUpserted(t *testing.T) {
	tests := []struct {
		name        string
		events      []outbox.Entry
		wantKey     string
		wantVersion int
		wantLen     int
	}{
		{
			name:        "created state updates cache",
			events:      []outbox.Entry{makeEntryUpserted("app.name", 1)},
			wantKey:     "app.name",
			wantVersion: 1,
			wantLen:     1,
		},
		{
			name: "updated state updates cache to latest version",
			events: []outbox.Entry{
				makeEntryUpserted("k", 1),
				makeEntryUpserted("k", 2),
			},
			wantKey:     "k",
			wantVersion: 2,
			wantLen:     1,
		},
		{
			name:        "version 5 is tracked",
			events:      []outbox.Entry{makeEntryUpserted("timeout", 5)},
			wantKey:     "timeout",
			wantVersion: 5,
			wantLen:     1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(slog.Default())

			for _, e := range tt.events {
				require.NoError(t, svc.HandleEntryUpserted(context.Background(), e))
			}

			assert.Equal(t, tt.wantLen, svc.Cache().Len())
			v, ok := svc.Cache().GetVersion(tt.wantKey)
			require.True(t, ok)
			assert.Equal(t, tt.wantVersion, v)
		})
	}
}

// TestService_HandleEntryUpserted_Monotonicity verifies that stale or replayed
// events (version <= known version) are ignored without overwriting the cache.
func TestService_HandleEntryUpserted_Monotonicity(t *testing.T) {
	svc := NewService(slog.Default())

	// v3 → v5 → v3 (replay): final state must be v5.
	require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 3)))
	require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 5)))
	require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 3)))

	v, ok := svc.Cache().GetVersion("k")
	require.True(t, ok)
	assert.Equal(t, 5, v, "stale/replayed event must not overwrite higher version")
}

func TestService_HandleEntryDeleted(t *testing.T) {
	svc := NewService(slog.Default())
	require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("k", 1)))
	require.NoError(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("k")))

	assert.Equal(t, 0, svc.Cache().Len())
	_, ok := svc.Cache().GetVersion("k")
	assert.False(t, ok)
}

// TestService_HandleEntryDeleted_NonExistentKey verifies that deleting a key that
// was never seen is a no-op (idempotent) and does not error.
func TestService_HandleEntryDeleted_NonExistentKey(t *testing.T) {
	svc := NewService(slog.Default())

	// No prior upsert — delete should succeed silently.
	require.NoError(t, svc.HandleEntryDeleted(context.Background(), makeEntryDeleted("nonexistent")))
	assert.Equal(t, 0, svc.Cache().Len())
}

func TestService_HandleEntryUpserted_InvalidPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantErr string
	}{
		{"invalid json", []byte("not-json"), "unmarshal"},
		{"missing key", []byte(`{"version":1}`), "missing key"},
		// value field must now be rejected (metadata-only schema)
		{"value field present", []byte(`{"key":"k","value":"v","version":1}`), "unknown field"},
		{"invalid version zero", []byte(`{"key":"k","version":0}`), "invalid version"},
		{"extra sensitive field", []byte(`{"key":"k","version":1,"sensitive":false}`), "unknown field"},
		{"old action field", []byte(`{"action":"updated","key":"k","version":1}`), "unknown field"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(slog.Default())
			entry := outbox.Entry{ID: "bad", Topic: domain.TopicConfigEntryUpserted, Payload: tt.payload}

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
			require.NoError(t, svc.HandleEntryUpserted(context.Background(), makeEntryUpserted("existing.key", 1)))

			entry := outbox.Entry{ID: "bad-delete", Topic: domain.TopicConfigEntryDeleted, Payload: tt.payload}
			err := svc.HandleEntryDeleted(context.Background(), entry)
			require.Error(t, err)

			var permErr *outbox.PermanentError
			require.ErrorAs(t, err, &permErr)
			assert.Equal(t, 1, svc.Cache().Len(), "cache must be unchanged after invalid delete")
			_, ok := svc.Cache().GetVersion("existing.key")
			require.True(t, ok)
		})
	}
}

// TestWrapLegacyHandler_Reject_Cases is a table-driven test covering both
// invalid JSON and the forbidden value field in a metadata-only payload.
func TestWrapLegacyHandler_Reject_Cases(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
	}{
		{"invalid json", []byte("not-json")},
		// Old wire format with value field — must be rejected
		{"value field present", []byte(`{"key":"k","value":"some-value","version":1}`)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(slog.Default())
			handler := outbox.WrapLegacyHandler(svc.HandleEntryUpserted)

			entry := outbox.Entry{ID: "bad", Topic: domain.TopicConfigEntryUpserted, Payload: tc.payload}
			result := handler(context.Background(), entry)

			assert.Equal(t, outbox.DispositionReject, result.Disposition)
			assert.Error(t, result.Err)
		})
	}
}
