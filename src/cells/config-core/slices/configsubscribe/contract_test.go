package configsubscribe

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Contract: event.config.changed.v1 subscribe — configsubscribe handles config.changed events.
func TestEventConfigChangedV1Subscribe(t *testing.T) {
	svc := NewService(slog.Default())

	entry := outbox.Entry{
		ID:        "evt-cfg-sub-01",
		EventType: TopicConfigChanged,
		Payload:   []byte(`{"action":"created","key":"app.name","value":"gocell","version":1}`),
	}
	err := svc.HandleEvent(context.Background(), entry)
	require.NoError(t, err, "contract: HandleEvent must accept well-formed config.changed payload")

	// Verify the cache was updated.
	val, ok := svc.Cache().Get("app.name")
	assert.True(t, ok, "contract: key must be cached after event")
	assert.Equal(t, "gocell", val)
}
