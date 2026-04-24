package eventrouter

import (
	"context"
	"sync"
	"testing"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- Spy tracer for AddContractHandler verification ---

type contractSpySpan struct {
	mu     sync.Mutex
	attrs  []wrapper.Attr
	status wrapper.StatusCode
	ended  bool
	name   string
}

func (s *contractSpySpan) SetAttributes(attrs ...wrapper.Attr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.attrs = append(s.attrs, attrs...)
}
func (s *contractSpySpan) RecordError(_ error) {}
func (s *contractSpySpan) SetStatus(code wrapper.StatusCode, _ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status = code
}
func (s *contractSpySpan) End() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ended = true
}
func (s *contractSpySpan) attrMap() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]any, len(s.attrs))
	for _, a := range s.attrs {
		out[a.Key] = a.Value
	}
	return out
}

type contractSpyTracer struct {
	mu    sync.Mutex
	spans []*contractSpySpan
}

func (t *contractSpyTracer) Start(ctx context.Context, name string, _ ...wrapper.Attr) (context.Context, wrapper.Span) {
	s := &contractSpySpan{name: name}
	t.mu.Lock()
	t.spans = append(t.spans, s)
	t.mu.Unlock()
	return ctx, s
}

func (t *contractSpyTracer) only(tb testing.TB) *contractSpySpan {
	tb.Helper()
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.spans) != 1 {
		tb.Fatalf("expected 1 span, got %d", len(t.spans))
	}
	return t.spans[0]
}

// --- Fixtures ---

func configChangedSpec() wrapper.ContractSpec {
	return wrapper.ContractSpec{
		ID:        "event.config.changed.v1",
		Kind:      "event",
		Transport: "amqp",
		Topic:     "event.config.changed.v1",
	}
}

func okHandler() outbox.EntryHandler {
	return func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}
}

// --- Guard tests ---

func TestAddContractHandler_NilHandler_Panics(t *testing.T) {
	t.Parallel()
	r := New(&blockingSubscriber{})
	assert.PanicsWithValue(t,
		"eventrouter: AddContractHandler called with nil handler",
		func() { r.AddContractHandler(configChangedSpec(), nil, "accesscore") },
	)
}

func TestAddContractHandler_EmptyConsumerGroup_Panics(t *testing.T) {
	t.Parallel()
	r := New(&blockingSubscriber{})
	assert.PanicsWithValue(t,
		"eventrouter: AddContractHandler called with empty consumerGroup; cells must declare their identity",
		func() { r.AddContractHandler(configChangedSpec(), okHandler(), "") },
	)
}

func TestAddContractHandler_NonEventSpec_PanicsViaWrapConsumer(t *testing.T) {
	t.Parallel()
	// Spec with Kind != "event" must be rejected by the wrapper.WrapConsumer
	// call inside AddContractHandler — registration-time fail-fast.
	httpSpec := wrapper.ContractSpec{
		ID: "http.x.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/x",
	}
	r := New(&blockingSubscriber{})
	defer func() {
		require.NotNil(t, recover(), "expected panic on non-event spec")
	}()
	r.AddContractHandler(httpSpec, okHandler(), "mycell")
}

// --- Happy-path + nil tracer fallback ---

func TestAddContractHandler_NilTracer_RegistersWithNoopWrapping(t *testing.T) {
	t.Parallel()
	// No WithTracer → r.tracer is nil; AddContractHandler must not panic —
	// WrapConsumer substitutes NoopTracer internally.
	r := New(&blockingSubscriber{})
	r.AddContractHandler(configChangedSpec(), okHandler(), "accesscore")
	assert.Equal(t, 1, r.HandlerCount(), "nil tracer path must still register handler")

	// The wrapped handler should still pass through Ack disposition unchanged.
	res := r.handlers[0].handler(context.Background(), outbox.Entry{})
	assert.Equal(t, outbox.DispositionAck, res.Disposition)
}

func TestAddContractHandler_TracerInjected_WrapsWithContractSpan(t *testing.T) {
	t.Parallel()
	tr := &contractSpyTracer{}
	r := New(&blockingSubscriber{})

	var inner bool
	r.AddContractHandler(configChangedSpec(), func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		inner = true
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}, "accesscore")
	require.Equal(t, 1, r.HandlerCount())

	// Drive one entry through the middleware position used by bootstrap:
	// contract tracing sits outside the stored business handler.
	sub := r.handlers[0].subscription()
	wrapped := ContractTracingMiddleware(tr, nil)(sub, r.handlers[0].handler)
	res := wrapped(context.Background(), outbox.Entry{EventType: "event.config.changed.v1"})
	assert.Equal(t, outbox.DispositionAck, res.Disposition)
	assert.True(t, inner, "inner handler must run")

	span := tr.only(t)
	attrs := span.attrMap()
	assert.Equal(t, "event.config.changed.v1", attrs["gocell.contract.id"], "gocell.contract.id")
	assert.Equal(t, "event", attrs["gocell.contract.kind"], "gocell.contract.kind")
	assert.Equal(t, "amqp", attrs["gocell.contract.transport"], "gocell.contract.transport")
	assert.Equal(t, "amqp", attrs["messaging.system"], "messaging.system")
	assert.Equal(t, "event.config.changed.v1", attrs["messaging.destination"], "messaging.destination")
	assert.Equal(t, "CONSUME event.config.changed.v1", span.name, "span name")
	assert.Equal(t, wrapper.StatusOK, span.status, "Ack must mark span StatusOK")
	assert.True(t, span.ended, "span.End() must have been called")
}

func TestContractTracingMiddleware_CoversDownstreamShortCircuit(t *testing.T) {
	t.Parallel()
	tr := &contractSpyTracer{}
	sub := outbox.Subscription{
		Topic:             "event.config.changed.v1",
		ConsumerGroup:     "accesscore",
		ContractID:        "event.config.changed.v1",
		ContractKind:      "event",
		ContractTransport: "amqp",
	}

	var businessCalled bool
	business := func(context.Context, outbox.Entry) outbox.HandleResult {
		businessCalled = true
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}
	shortCircuit := func(_ outbox.Subscription, _ outbox.EntryHandler) outbox.EntryHandler {
		return func(context.Context, outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{Disposition: outbox.DispositionRequeue}
		}
	}

	wrapped := ContractTracingMiddleware(tr, nil)(sub, shortCircuit(sub, business))
	res := wrapped(context.Background(), outbox.Entry{EventType: "event.config.changed.v1"})

	assert.Equal(t, outbox.DispositionRequeue, res.Disposition)
	assert.False(t, businessCalled, "downstream middleware should be allowed to skip business handler")

	span := tr.only(t)
	assert.Equal(t, wrapper.StatusError, span.status, "short-circuit Requeue must still mark the contract span")
	assert.True(t, span.ended, "span.End() must have been called")
	assert.Equal(t, "event.config.changed.v1", span.attrMap()["gocell.contract.id"])
}

func TestAddContractHandler_MultipleRegistrations_HandlersGrow(t *testing.T) {
	t.Parallel()
	tr := &contractSpyTracer{}
	r := New(&blockingSubscriber{}, WithTracer(tr))
	for i := 0; i < 3; i++ {
		spec := configChangedSpec()
		spec.Topic = spec.Topic + "." + string(rune('a'+i))
		r.AddContractHandler(spec, okHandler(), "accesscore")
	}
	assert.Equal(t, 3, r.HandlerCount())
}

// TestAddContractHandler_HandlerConfigShape verifies the wrapped handler is
// stored under spec.Topic (not a separate topic arg) and the consumerGroup
// is preserved.
func TestAddContractHandler_HandlerConfigShape(t *testing.T) {
	t.Parallel()
	r := New(&blockingSubscriber{})
	r.AddContractHandler(configChangedSpec(), okHandler(), "accesscore")
	require.Equal(t, 1, len(r.handlers))
	cfg := r.handlers[0]
	assert.Equal(t, "event.config.changed.v1", cfg.topic, "topic derived from spec.Topic")
	assert.Equal(t, "accesscore", cfg.consumerGroup, "consumerGroup preserved")
	assert.NotNil(t, cfg.handler, "handler stored")
}
