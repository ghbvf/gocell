package outbox

import (
	"context"
	"errors"
	"fmt"
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

func (m *mockSubscriber) Subscribe(ctx context.Context, topic string, handler EntryHandler) error {
	return nil
}
func (m *mockSubscriber) Close() error { return nil }

var _ Subscriber = (*mockSubscriber)(nil)

func TestSubscriberInterface(t *testing.T) {
	var sub Subscriber = &mockSubscriber{}

	t.Run("Subscribe returns nil on success", func(t *testing.T) {
		handler := func(ctx context.Context, entry Entry) HandleResult {
			return HandleResult{Disposition: DispositionAck}
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
	capturedHandler EntryHandler
	closeErr        error
}

func (r *recordingSubscriber) Subscribe(_ context.Context, topic string, handler EntryHandler) error {
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
	handler := func(_ context.Context, _ Entry) HandleResult {
		called = true
		return HandleResult{Disposition: DispositionAck}
	}

	err := sub.Subscribe(context.Background(), "test.topic", handler)
	assert.NoError(t, err)
	assert.True(t, inner.subscribeCalled)
	assert.Equal(t, "test.topic", inner.subscribeTopic)

	// Call the captured handler to verify it's the original.
	res := inner.capturedHandler(context.Background(), Entry{})
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.True(t, called)
}

func TestSubscriberWithMiddleware_SingleMiddleware(t *testing.T) {
	inner := &recordingSubscriber{}

	var middlewareTopic string
	middleware := func(topic string, next EntryHandler) EntryHandler {
		middlewareTopic = topic
		return func(ctx context.Context, e Entry) HandleResult {
			e.Metadata = map[string]string{"wrapped": "true"}
			return next(ctx, e)
		}
	}

	sub := &SubscriberWithMiddleware{
		Inner:      inner,
		Middleware: []TopicHandlerMiddleware{middleware},
	}

	var receivedEntry Entry
	handler := func(_ context.Context, e Entry) HandleResult {
		receivedEntry = e
		return HandleResult{Disposition: DispositionAck}
	}

	err := sub.Subscribe(context.Background(), "orders.created", handler)
	assert.NoError(t, err)
	assert.Equal(t, "orders.created", middlewareTopic)

	// Call captured handler to verify middleware was applied.
	res := inner.capturedHandler(context.Background(), Entry{ID: "evt-1"})
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.Equal(t, "evt-1", receivedEntry.ID)
	assert.Equal(t, "true", receivedEntry.Metadata["wrapped"])
}

func TestSubscriberWithMiddleware_MultipleMiddleware_OrderCorrect(t *testing.T) {
	inner := &recordingSubscriber{}

	var order []string

	makeMiddleware := func(name string) TopicHandlerMiddleware {
		return func(topic string, next EntryHandler) EntryHandler {
			return func(ctx context.Context, e Entry) HandleResult {
				order = append(order, name+"-before")
				res := next(ctx, e)
				order = append(order, name+"-after")
				return res
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

	handler := func(_ context.Context, _ Entry) HandleResult {
		order = append(order, "handler")
		return HandleResult{Disposition: DispositionAck}
	}

	err := sub.Subscribe(context.Background(), "test.topic", handler)
	assert.NoError(t, err)

	_ = inner.capturedHandler(context.Background(), Entry{})

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

	shortCircuit := func(_ string, _ EntryHandler) EntryHandler {
		return func(_ context.Context, _ Entry) HandleResult {
			return HandleResult{
				Disposition: DispositionReject,
				Err:         assert.AnError,
			}
		}
	}

	sub := &SubscriberWithMiddleware{
		Inner:      inner,
		Middleware: []TopicHandlerMiddleware{shortCircuit},
	}

	handlerCalled := false
	handler := func(_ context.Context, _ Entry) HandleResult {
		handlerCalled = true
		return HandleResult{Disposition: DispositionAck}
	}

	err := sub.Subscribe(context.Background(), "test.topic", handler)
	assert.NoError(t, err)

	// Call captured handler — middleware should short-circuit.
	res := inner.capturedHandler(context.Background(), Entry{})
	assert.Equal(t, DispositionReject, res.Disposition)
	assert.Error(t, res.Err)
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
			name:      "Both empty — returns empty string",
			entry:     Entry{},
			wantTopic: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantTopic, tt.entry.RoutingTopic())
		})
	}
}

// --- Disposition Tests ---

func TestDisposition_String(t *testing.T) {
	tests := []struct {
		d    Disposition
		want string
	}{
		{Disposition(0), "invalid"},
		{DispositionAck, "ack"},
		{DispositionRequeue, "requeue"},
		{DispositionReject, "reject"},
		{Disposition(99), "disposition(99)"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			assert.Equal(t, tt.want, tt.d.String())
		})
	}
}

func TestDisposition_Valid(t *testing.T) {
	tests := []struct {
		d    Disposition
		want bool
	}{
		{Disposition(0), false},
		{DispositionAck, true},
		{DispositionRequeue, true},
		{DispositionReject, true},
		{Disposition(99), false},
	}
	for _, tt := range tests {
		t.Run(tt.d.String(), func(t *testing.T) {
			assert.Equal(t, tt.want, tt.d.Valid())
		})
	}
}

func TestDisposition_ZeroValueIsNotAck(t *testing.T) {
	// R-1: The zero value of Disposition MUST NOT equal DispositionAck.
	// A forgotten/uninitialised HandleResult.Disposition must not silently ACK.
	var zero Disposition
	assert.NotEqual(t, DispositionAck, zero,
		"zero-value Disposition must differ from DispositionAck")
	assert.Equal(t, "invalid", zero.String(),
		"zero-value Disposition.String() must return \"invalid\"")
	assert.False(t, zero.Valid(),
		"zero-value Disposition must not be valid")
}

func TestHandleResult_ZeroValueDispositionIsInvalid(t *testing.T) {
	// R-1: HandleResult{} (zero value) must have an invalid Disposition.
	var res HandleResult
	assert.NotEqual(t, DispositionAck, res.Disposition)
	assert.False(t, res.Disposition.Valid())
}

// --- WrapLegacyHandler Tests ---

func TestWrapLegacyHandler_Success(t *testing.T) {
	legacy := func(_ context.Context, _ Entry) error { return nil }
	handler := WrapLegacyHandler(legacy)

	res := handler(context.Background(), Entry{ID: "1"})
	assert.Equal(t, DispositionAck, res.Disposition)
	assert.NoError(t, res.Err)
}

func TestWrapLegacyHandler_Error(t *testing.T) {
	legacy := func(_ context.Context, _ Entry) error { return assert.AnError }
	handler := WrapLegacyHandler(legacy)

	res := handler(context.Background(), Entry{ID: "1"})
	assert.Equal(t, DispositionRequeue, res.Disposition)
	assert.Equal(t, assert.AnError, res.Err)
}

func TestWrapLegacyHandler_PermanentError(t *testing.T) {
	legacy := func(_ context.Context, _ Entry) error {
		return NewPermanentError(errors.New("unmarshal failed"))
	}
	handler := WrapLegacyHandler(legacy)

	res := handler(context.Background(), Entry{ID: "1"})
	assert.Equal(t, DispositionReject, res.Disposition)
	assert.Error(t, res.Err)

	var permErr *PermanentError
	assert.True(t, errors.As(res.Err, &permErr))
}

func TestWrapLegacyHandler_WrappedPermanentError(t *testing.T) {
	legacy := func(_ context.Context, _ Entry) error {
		return fmt.Errorf("handler context: %w", NewPermanentError(errors.New("bad payload")))
	}
	handler := WrapLegacyHandler(legacy)

	res := handler(context.Background(), Entry{ID: "1"})
	assert.Equal(t, DispositionReject, res.Disposition,
		"wrapped PermanentError must be detected via errors.As")
}

// --- PermanentError Tests ---

func TestPermanentError(t *testing.T) {
	inner := errors.New("bad payload")
	pe := NewPermanentError(inner)

	assert.Equal(t, "permanent: bad payload", pe.Error())
	assert.Equal(t, inner, pe.Unwrap())
	assert.ErrorIs(t, pe, inner)
}

func TestPermanentError_ErrorsAs_ThroughWrapping(t *testing.T) {
	inner := errors.New("decode error")
	pe := NewPermanentError(inner)
	wrapped := fmt.Errorf("handler: %w", pe)

	var target *PermanentError
	assert.True(t, errors.As(wrapped, &target))
	assert.Equal(t, inner, target.Err)
}

// --- Entry.Validate Tests (F-OB-03) ---

func TestEntry_Validate(t *testing.T) {
	tests := []struct {
		name    string
		entry   Entry
		wantErr bool
	}{
		{
			name:    "valid with Topic",
			entry:   Entry{Topic: "t", Payload: []byte("{}")},
			wantErr: false,
		},
		{
			name:    "valid with EventType fallback",
			entry:   Entry{EventType: "e", Payload: []byte("{}")},
			wantErr: false,
		},
		{
			name:    "missing topic and EventType",
			entry:   Entry{Payload: []byte("{}")},
			wantErr: true,
		},
		{
			name:    "missing payload",
			entry:   Entry{Topic: "t"},
			wantErr: true,
		},
		{
			name:    "completely empty",
			entry:   Entry{},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.entry.Validate()
			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), "ERR_VALIDATION_FAILED")
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// --- HandleResult tests ---

func TestHandleResult_Fields(t *testing.T) {
	res := HandleResult{
		Disposition: DispositionReject,
		Err:         assert.AnError,
		Receipt:     nil,
	}
	assert.Equal(t, DispositionReject, res.Disposition)
	assert.Error(t, res.Err)
	assert.Nil(t, res.Receipt)
}
