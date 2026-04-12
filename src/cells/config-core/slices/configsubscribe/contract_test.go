package configsubscribe

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Contract: event.config.changed.v1 subscribe — handles created/updated/deleted/published.
func TestEventConfigChangedV1Subscribe(t *testing.T) {
	svc := NewService(slog.Default())

	entry := outbox.Entry{
		ID:        "evt-cfg-sub-01",
		EventType: TopicConfigChanged,
		Payload:   []byte(`{"action":"created","key":"app.name","value":"gocell","version":1}`),
	}
	err := svc.HandleEvent(context.Background(), entry)
	require.NoError(t, err, "contract: HandleEvent must accept well-formed config.changed payload")

	val, ok := svc.Cache().Get("app.name")
	assert.True(t, ok, "contract: key must be cached after created event")
	assert.Equal(t, "gocell", val)
}

// Regression: published action must NOT overwrite cache with empty string.
// configpublish sends {action: "published", key, config_id, version} without value field.
func TestEventConfigChangedV1Subscribe_PublishedDoesNotClobberCache(t *testing.T) {
	svc := NewService(slog.Default())

	// Seed cache via a created event.
	err := svc.HandleEvent(context.Background(), outbox.Entry{
		ID: "evt-1", EventType: TopicConfigChanged,
		Payload: []byte(`{"action":"created","key":"db.host","value":"localhost","version":1}`),
	})
	require.NoError(t, err)

	val, _ := svc.Cache().Get("db.host")
	require.Equal(t, "localhost", val)

	// Published event has no value field — must not overwrite cache.
	err = svc.HandleEvent(context.Background(), outbox.Entry{
		ID: "evt-2", EventType: TopicConfigChanged,
		Payload: []byte(`{"action":"published","key":"db.host","config_id":"cfg-1","version":2}`),
	})
	require.NoError(t, err)

	val, ok := svc.Cache().Get("db.host")
	assert.True(t, ok, "key must still be in cache after published event")
	assert.Equal(t, "localhost", val, "published event must not overwrite cached value with empty string")
}
