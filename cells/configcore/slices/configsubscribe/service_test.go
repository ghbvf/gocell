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

func makeEntryWritten(action domain.ConfigEntryWrittenAction, key, value string) outbox.Entry {
	payload, _ := json.Marshal(domain.ConfigEntryWrittenEvent{
		Action: action,
		Key:    key,
		Value:  value,
	})
	return outbox.Entry{ID: "test-1", Payload: payload}
}

func TestService_HandleEntryWritten(t *testing.T) {
	tests := []struct {
		name      string
		events    []outbox.Entry
		wantKey   string
		wantValue string
		wantLen   int
	}{
		{
			name:      "created event updates cache",
			events:    []outbox.Entry{makeEntryWritten(domain.ConfigEntryActionCreated, "app.name", "gocell")},
			wantKey:   "app.name",
			wantValue: "gocell",
			wantLen:   1,
		},
		{
			name: "updated event updates cache",
			events: []outbox.Entry{
				makeEntryWritten(domain.ConfigEntryActionCreated, "k", "v1"),
				makeEntryWritten(domain.ConfigEntryActionUpdated, "k", "v2"),
			},
			wantKey:   "k",
			wantValue: "v2",
			wantLen:   1,
		},
		{
			name: "deleted event removes from cache",
			events: []outbox.Entry{
				makeEntryWritten(domain.ConfigEntryActionCreated, "k", "v"),
				makeEntryWritten(domain.ConfigEntryActionDeleted, "k", ""),
			},
			wantKey: "k",
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(slog.Default())

			for _, e := range tt.events {
				err := svc.HandleEntryWritten(context.Background(), e)
				require.NoError(t, err)
			}

			assert.Equal(t, tt.wantLen, svc.Cache().Len())
			if tt.wantLen > 0 {
				v, ok := svc.Cache().Get(tt.wantKey)
				assert.True(t, ok)
				assert.Equal(t, tt.wantValue, v)
			}
		})
	}
}

func TestService_HandleEntryWritten_InvalidPayload(t *testing.T) {
	svc := NewService(slog.Default())
	entry := outbox.Entry{ID: "bad", Payload: []byte("not-json")}

	// Should return PermanentError so WrapLegacyHandler routes to DLX, not retry.
	err := svc.HandleEntryWritten(context.Background(), entry)
	require.Error(t, err)
	assert.Equal(t, 0, svc.Cache().Len())

	var permErr *outbox.PermanentError
	require.ErrorAs(t, err, &permErr, "invalid payload must be PermanentError")
}

// TestWrapLegacyHandler_InvalidPayload_Reject verifies the full disposition
// chain: invalid payload → PermanentError → WrapLegacyHandler → DispositionReject.
func TestWrapLegacyHandler_InvalidPayload_Reject(t *testing.T) {
	svc := NewService(slog.Default())
	handler := outbox.WrapLegacyHandler(svc.HandleEntryWritten)

	entry := outbox.Entry{ID: "bad", Payload: []byte("not-json")}
	result := handler(context.Background(), entry)

	assert.Equal(t, outbox.DispositionReject, result.Disposition,
		"invalid payload via WrapLegacyHandler must produce DispositionReject")
	assert.Error(t, result.Err)
}

// TestHandleEntryWritten_UnknownAction_PermanentError verifies that an unknown action
// returns a PermanentError (fail-closed, P1-14 A3). The cache must not be modified.
func TestHandleEntryWritten_UnknownAction_PermanentError(t *testing.T) {
	svc := NewService(slog.Default())
	// Pre-populate cache so we can verify it stays unchanged.
	_ = svc.HandleEntryWritten(context.Background(), makeEntryWritten(domain.ConfigEntryActionCreated, "existing.key", "existing-value"))

	entry := makeEntryWritten(domain.ConfigEntryWrittenAction("bogus"), "some.key", "")
	err := svc.HandleEntryWritten(context.Background(), entry)

	require.Error(t, err, "unknown action must return error")

	// Must be a PermanentError so WrapLegacyHandler routes to DLX, not retry.
	var permErr *outbox.PermanentError
	require.ErrorAs(t, err, &permErr, "unknown action must be PermanentError")
	assert.Contains(t, err.Error(), "bogus", "error message should include the unknown action name")

	// Cache must not be modified.
	assert.Equal(t, 1, svc.Cache().Len(), "cache must be unchanged after unknown action")
	v, ok := svc.Cache().Get("existing.key")
	assert.True(t, ok)
	assert.Equal(t, "existing-value", v)
}

// TestHandleEntryWritten_UnknownAction_WrapLegacyHandler_Reject verifies the full
// disposition chain: unknown action → PermanentError → WrapLegacyHandler → DispositionReject.
func TestHandleEntryWritten_UnknownAction_WrapLegacyHandler_Reject(t *testing.T) {
	svc := NewService(slog.Default())
	handler := outbox.WrapLegacyHandler(svc.HandleEntryWritten)

	entry := makeEntryWritten(domain.ConfigEntryWrittenAction("bogus"), "some.key", "")
	result := handler(context.Background(), entry)

	assert.Equal(t, outbox.DispositionReject, result.Disposition,
		"unknown action via WrapLegacyHandler must produce DispositionReject")
	assert.Error(t, result.Err)
}

// TestService_HandleVersionPublished confirms the version-published handler
// decodes the typed payload and does not touch the cache.
func TestService_HandleVersionPublished(t *testing.T) {
	svc := NewService(slog.Default())
	// Seed the cache — the version-published handler must leave it alone.
	_ = svc.HandleEntryWritten(context.Background(), makeEntryWritten(domain.ConfigEntryActionCreated, "seeded", "val"))

	payload, err := json.Marshal(domain.ConfigVersionPublishedEvent{
		Key:       "seeded",
		ConfigID:  "cfg-123",
		Version:   2,
		Sensitive: false,
	})
	require.NoError(t, err)
	entry := outbox.Entry{ID: "vp-1", Payload: payload}

	require.NoError(t, svc.HandleVersionPublished(context.Background(), entry))
	assert.Equal(t, 1, svc.Cache().Len(), "version-published must not touch cache")
	v, ok := svc.Cache().Get("seeded")
	require.True(t, ok)
	assert.Equal(t, "val", v)
}

func TestService_HandleVersionPublished_InvalidPayload(t *testing.T) {
	svc := NewService(slog.Default())
	entry := outbox.Entry{ID: "bad", Payload: []byte("not-json")}

	err := svc.HandleVersionPublished(context.Background(), entry)
	require.Error(t, err)

	var permErr *outbox.PermanentError
	require.ErrorAs(t, err, &permErr, "invalid payload must be PermanentError")
}
