package configreceive

import (
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubConfigClient is a test double for ports.ConfigClient.
type stubConfigClient struct {
	entry      ports.ConfigEntry
	err        error
	calledWith string // records the key argument passed to GetEntry
}

func (s *stubConfigClient) GetEntry(_ context.Context, key string) (ports.ConfigEntry, error) {
	s.calledWith = key
	return s.entry, s.err
}

func TestHandleEntryUpserted_ValidPayload(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
	}{
		{"metadata-only key+version", []byte(`{"key":"jwt.ttl","version":1}`)},
		{"higher version", []byte(`{"key":"jwt.ttl","version":42}`)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(slog.Default())
			entry := outbox.Entry{
				ID:      "evt-1",
				Topic:   TopicConfigEntryUpserted,
				Payload: tt.payload,
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
		{"missing key", []byte(`{"version":1}`), "missing key"},
		{"empty key", []byte(`{"key":"","version":1}`), "missing key"},
		{"blank key whitespace", []byte(`{"key":"   ","version":1}`), "missing key"},
		{"missing version", []byte(`{"key":"jwt.ttl"}`), "invalid version"},
		{"invalid version zero", []byte(`{"key":"jwt.ttl","version":0}`), "invalid version"},
		// value field is rejected — payload must be metadata-only
		{"value field present", []byte(`{"key":"jwt.ttl","value":"30m","version":1}`), "unknown field"},
		{"extra sensitive field", []byte(`{"key":"jwt.ttl","version":1,"sensitive":false}`), "unknown field"},
		{"old action field", []byte(`{"action":"updated","key":"jwt.ttl","version":1}`), "unknown field"},
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
	entry := outbox.Entry{
		ID:      "evt-del-1",
		Topic:   TopicConfigEntryDeleted,
		Payload: []byte(`{"key":"jwt.ttl","version":3}`),
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
		{"missing key", []byte(`{"version":1}`), "missing key"},
		{"missing version", []byte(`{"key":"jwt.ttl"}`), "invalid version"},
		{"version zero", []byte(`{"key":"jwt.ttl","version":0}`), "invalid version"},
		{"extra value field", []byte(`{"key":"jwt.ttl","version":1,"value":"old"}`), "unknown field"},
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

	entry := outbox.Entry{
		ID:      "evt-wrap-1",
		Topic:   TopicConfigEntryUpserted,
		Payload: []byte(`{"key":"jwt.ttl","version":1}`),
	}
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

func TestWrapLegacyHandler_EntryUpserted_ValueField_Reject(t *testing.T) {
	svc := NewService(slog.Default())
	handler := outbox.WrapLegacyHandler(svc.HandleEntryUpserted)

	// value field is now rejected — metadata-only schema
	entry := outbox.Entry{
		ID:      "evt-wrap-3",
		Topic:   TopicConfigEntryUpserted,
		Payload: []byte(`{"key":"jwt.ttl","value":"30m","version":1}`),
	}
	result := handler(context.Background(), entry)

	assert.Equal(t, outbox.DispositionReject, result.Disposition)
	assert.Error(t, result.Err)
}

func TestHandleEntryUpserted_WithConfigClient_FetchOK(t *testing.T) {
	stub := &stubConfigClient{
		entry: ports.ConfigEntry{Key: "jwt.ttl", Value: "30m", Version: 2},
	}
	svc := NewService(slog.Default(), WithConfigClient(stub))

	entry := outbox.Entry{
		ID:      "evt-cfg-1",
		Topic:   TopicConfigEntryUpserted,
		Payload: []byte(`{"key":"jwt.ttl","version":2,"actorId":"adm-1"}`),
	}
	err := svc.HandleEntryUpserted(context.Background(), entry)
	require.NoError(t, err)
	// F5: assert stub was called with the correct key from the event payload.
	assert.Equal(t, "jwt.ttl", stub.calledWith, "ConfigClient.GetEntry must be called with the event's key")
}

// TestHandleEntryUpserted_WithConfigClient_FetchError asserts that a transient
// fetch error (non-404) causes HandleEntryUpserted to return an error so the
// legacy handler wrapper triggers Requeue instead of silently Acking.
func TestHandleEntryUpserted_WithConfigClient_FetchError(t *testing.T) {
	stub := &stubConfigClient{
		err: errors.New("configcore unavailable"),
	}
	svc := NewService(slog.Default(), WithConfigClient(stub))

	entry := outbox.Entry{
		ID:      "evt-cfg-2",
		Topic:   TopicConfigEntryUpserted,
		Payload: []byte(`{"key":"jwt.ttl","version":1,"actorId":"adm-1"}`),
	}
	// Transient fetch failure must return a non-nil error → Requeue (not Ack).
	err := svc.HandleEntryUpserted(context.Background(), entry)
	require.Error(t, err, "transient fetch failure must return non-nil error to trigger Requeue")
	assert.Equal(t, "jwt.ttl", stub.calledWith, "ConfigClient.GetEntry must be called with the event's key")
}

// TestHandleEntryUpserted_WithConfigClient_FetchNotFound asserts that a 404
// (config entry genuinely gone) is treated as a stale event: log Warn + Ack
// (returning nil), since retrying cannot help when the entry no longer exists.
func TestHandleEntryUpserted_WithConfigClient_FetchNotFound(t *testing.T) {
	stub := &stubConfigClient{
		err: errcode.NewDomain(errcode.ErrConfigNotFound, "config entry not found"),
	}
	svc := NewService(slog.Default(), WithConfigClient(stub))

	entry := outbox.Entry{
		ID:      "evt-cfg-404",
		Topic:   TopicConfigEntryUpserted,
		Payload: []byte(`{"key":"jwt.ttl","version":1,"actorId":"adm-1"}`),
	}
	// 404 → stale event → Ack (nil error), no retry.
	err := svc.HandleEntryUpserted(context.Background(), entry)
	require.NoError(t, err, "not-found fetch error must return nil (stale event, no retry needed)")
}

func TestHandleEntryUpserted_WithoutConfigClient_NoFetch(t *testing.T) {
	// Nil configClient — service must function correctly in log-only mode.
	svc := NewService(slog.Default())

	entry := outbox.Entry{
		ID:      "evt-cfg-3",
		Topic:   TopicConfigEntryUpserted,
		Payload: []byte(`{"key":"jwt.ttl","version":1,"actorId":"adm-1"}`),
	}
	err := svc.HandleEntryUpserted(context.Background(), entry)
	require.NoError(t, err)
}
