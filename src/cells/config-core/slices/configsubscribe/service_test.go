package configsubscribe

import (
	"context"
	"encoding/json"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func makeEntry(action, key, value string) outbox.Entry {
	payload, _ := json.Marshal(map[string]any{
		"action": action,
		"key":    key,
		"value":  value,
	})
	return outbox.Entry{ID: "test-1", Payload: payload}
}

func TestService_HandleEvent(t *testing.T) {
	tests := []struct {
		name      string
		events    []outbox.Entry
		wantKey   string
		wantValue string
		wantLen   int
	}{
		{
			name:      "created event updates cache",
			events:    []outbox.Entry{makeEntry("created", "app.name", "gocell")},
			wantKey:   "app.name",
			wantValue: "gocell",
			wantLen:   1,
		},
		{
			name:      "updated event updates cache",
			events:    []outbox.Entry{makeEntry("created", "k", "v1"), makeEntry("updated", "k", "v2")},
			wantKey:   "k",
			wantValue: "v2",
			wantLen:   1,
		},
		{
			name:    "deleted event removes from cache",
			events:  []outbox.Entry{makeEntry("created", "k", "v"), makeEntry("deleted", "k", "")},
			wantKey: "k",
			wantLen: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(slog.Default())

			for _, e := range tt.events {
				err := svc.HandleEvent(context.Background(), e)
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

func TestService_HandleEvent_InvalidPayload(t *testing.T) {
	svc := NewService(slog.Default())
	entry := outbox.Entry{ID: "bad", Payload: []byte("not-json")}

	// Should return error so ConsumerBase routes to dead letter after retries.
	err := svc.HandleEvent(context.Background(), entry)
	assert.Error(t, err)
	assert.Equal(t, 0, svc.Cache().Len())
}
