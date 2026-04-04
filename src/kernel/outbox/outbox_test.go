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
