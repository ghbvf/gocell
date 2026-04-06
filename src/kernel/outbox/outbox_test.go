package outbox

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// Compile-time interface checks.

type mockWriter struct{}

func (m *mockWriter) Write(ctx context.Context, entry Entry) error { return nil }

var _ Writer = (*mockWriter)(nil)

type mockRelay struct{}

func (m *mockRelay) Start(ctx context.Context) error { return nil }
func (m *mockRelay) Stop(ctx context.Context) error  { return nil }

var _ Relay = (*mockRelay)(nil)

type mockPublisher struct{}

func (m *mockPublisher) Publish(ctx context.Context, topic string, payload []byte) error { return nil }

var _ Publisher = (*mockPublisher)(nil)

type mockSubscriber struct{}

func (m *mockSubscriber) Subscribe(ctx context.Context, topic string, handler func(context.Context, Entry) error) error {
	return nil
}
func (m *mockSubscriber) Close() error { return nil }

var _ Subscriber = (*mockSubscriber)(nil)

func TestSubscriberInterface(t *testing.T) {
	var sub Subscriber = &mockSubscriber{}

	t.Run("Subscribe returns nil on success", func(t *testing.T) {
		handler := func(ctx context.Context, entry Entry) error {
			return nil
		}
		err := sub.Subscribe(context.Background(), "test.topic", handler)
		assert.NoError(t, err)
	})

	t.Run("Close returns nil on success", func(t *testing.T) {
		err := sub.Close()
		assert.NoError(t, err)
	})
}

func TestEntryFields(t *testing.T) {
	e := Entry{
		ID:            "1",
		AggregateID:   "a",
		AggregateType: "order",
		EventType:     "created",
		Payload:       []byte("{}"),
		CreatedAt:     time.Now(),
	}
	assert.NotEmpty(t, e.ID)
	assert.NotEmpty(t, e.AggregateID)
	assert.NotEmpty(t, e.AggregateType)
	assert.NotEmpty(t, e.EventType)
	assert.NotEmpty(t, e.Payload)
	assert.False(t, e.CreatedAt.IsZero())
}

func TestEntry_RoutingTopic(t *testing.T) {
	tests := []struct {
		name      string
		entry     Entry
		wantTopic string
	}{
		{
			name: "Topic set — returns Topic",
			entry: Entry{
				EventType: "order.created",
				Topic:     "orders.v2",
			},
			wantTopic: "orders.v2",
		},
		{
			name: "Topic empty — falls back to EventType",
			entry: Entry{
				EventType: "order.created",
				Topic:     "",
			},
			wantTopic: "order.created",
		},
		{
			name: "Topic zero value (not set) — falls back to EventType",
			entry: Entry{
				EventType: "session.created",
			},
			wantTopic: "session.created",
		},
		{
			name: "Both empty — returns empty string",
			entry: Entry{},
			wantTopic: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantTopic, tt.entry.RoutingTopic())
		})
	}
}
