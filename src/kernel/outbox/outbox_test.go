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

// --- SubscriberWithMiddleware Tests ---

// recordingSubscriber captures the handler passed to Subscribe so tests can inspect it.
type recordingSubscriber struct {
	subscribeCalled bool
	subscribeTopic  string
	capturedHandler func(context.Context, Entry) error
	closeErr        error
}

func (r *recordingSubscriber) Subscribe(_ context.Context, topic string, handler func(context.Context, Entry) error) error {
	r.subscribeCalled = true
	r.subscribeTopic = topic
	r.capturedHandler = handler
	return nil
}

func (r *recordingSubscriber) Close() error {
	return r.closeErr
}

var _ Subscriber = (*recordingSubscriber)(nil)

func TestSubscriberWithMiddleware_InterfaceCompliance(t *testing.T) {
	var _ Subscriber = (*SubscriberWithMiddleware)(nil)
}

func TestSubscriberWithMiddleware_NoMiddleware(t *testing.T) {
	inner := &recordingSubscriber{}
	sub := &SubscriberWithMiddleware{Inner: inner}

	called := false
	handler := func(_ context.Context, _ Entry) error {
		called = true
		return nil
	}

	err := sub.Subscribe(context.Background(), "test.topic", handler)
	assert.NoError(t, err)
	assert.True(t, inner.subscribeCalled)
	assert.Equal(t, "test.topic", inner.subscribeTopic)

	// Call the captured handler to verify it's the original.
	err = inner.capturedHandler(context.Background(), Entry{})
	assert.NoError(t, err)
	assert.True(t, called)
}

func TestSubscriberWithMiddleware_SingleMiddleware(t *testing.T) {
	inner := &recordingSubscriber{}

	var middlewareTopic string
	middleware := func(topic string, next func(context.Context, Entry) error) func(context.Context, Entry) error {
		middlewareTopic = topic
		return func(ctx context.Context, e Entry) error {
			e.Metadata = map[string]string{"wrapped": "true"}
			return next(ctx, e)
		}
	}

	sub := &SubscriberWithMiddleware{
		Inner:      inner,
		Middleware: []TopicHandlerMiddleware{middleware},
	}

	var receivedEntry Entry
	handler := func(_ context.Context, e Entry) error {
		receivedEntry = e
		return nil
	}

	err := sub.Subscribe(context.Background(), "orders.created", handler)
	assert.NoError(t, err)
	assert.Equal(t, "orders.created", middlewareTopic)

	// Call captured handler to verify middleware was applied.
	err = inner.capturedHandler(context.Background(), Entry{ID: "evt-1"})
	assert.NoError(t, err)
	assert.Equal(t, "evt-1", receivedEntry.ID)
	assert.Equal(t, "true", receivedEntry.Metadata["wrapped"])
}

func TestSubscriberWithMiddleware_MultipleMiddleware_OrderCorrect(t *testing.T) {
	inner := &recordingSubscriber{}

	var order []string

	makeMiddleware := func(name string) TopicHandlerMiddleware {
		return func(topic string, next func(context.Context, Entry) error) func(context.Context, Entry) error {
			return func(ctx context.Context, e Entry) error {
				order = append(order, name+"-before")
				err := next(ctx, e)
				order = append(order, name+"-after")
				return err
			}
		}
	}

	sub := &SubscriberWithMiddleware{
		Inner: inner,
		Middleware: []TopicHandlerMiddleware{
			makeMiddleware("outer"),
			makeMiddleware("inner"),
		},
	}

	handler := func(_ context.Context, _ Entry) error {
		order = append(order, "handler")
		return nil
	}

	err := sub.Subscribe(context.Background(), "test.topic", handler)
	assert.NoError(t, err)

	err = inner.capturedHandler(context.Background(), Entry{})
	assert.NoError(t, err)

	// [0] is outermost, [len-1] is innermost.
	assert.Equal(t, []string{
		"outer-before",
		"inner-before",
		"handler",
		"inner-after",
		"outer-after",
	}, order)
}

func TestSubscriberWithMiddleware_Close_DelegatesToInner(t *testing.T) {
	inner := &recordingSubscriber{}
	sub := &SubscriberWithMiddleware{Inner: inner}

	err := sub.Close()
	assert.NoError(t, err)
}

func TestSubscriberWithMiddleware_Close_PropagatesError(t *testing.T) {
	inner := &recordingSubscriber{closeErr: assert.AnError}
	sub := &SubscriberWithMiddleware{Inner: inner}

	err := sub.Close()
	assert.Error(t, err)
	assert.Equal(t, assert.AnError, err)
}

func TestSubscriberWithMiddleware_MiddlewareCanShortCircuit(t *testing.T) {
	inner := &recordingSubscriber{}

	shortCircuit := func(_ string, _ func(context.Context, Entry) error) func(context.Context, Entry) error {
		return func(_ context.Context, _ Entry) error {
			return assert.AnError
		}
	}

	sub := &SubscriberWithMiddleware{
		Inner:      inner,
		Middleware: []TopicHandlerMiddleware{shortCircuit},
	}

	handlerCalled := false
	handler := func(_ context.Context, _ Entry) error {
		handlerCalled = true
		return nil
	}

	err := sub.Subscribe(context.Background(), "test.topic", handler)
	assert.NoError(t, err)

	// Call captured handler — middleware should short-circuit.
	err = inner.capturedHandler(context.Background(), Entry{})
	assert.Error(t, err)
	assert.False(t, handlerCalled)
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
