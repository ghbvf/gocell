package eventrouter

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
)

// --- Spy tracer for ContractTracingMiddleware verification ---

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

func configEntryUpsertedSpec() wrapper.ContractSpec {
	return wrapper.ContractSpec{
		ID:        "event.config.entry-upserted.v1",
		Kind:      "event",
		Transport: "amqp",
		Topic:     "event.config.entry-upserted.v1",
	}
}

func okHandler() outbox.EntryHandler {
	return func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}
}

// --- Guard tests ---

func TestAddContractHandler_NilHandler_ReturnsError(t *testing.T) {
	t.Parallel()
	r := New(&blockingSubscriber{}, clock.Real())
	err := r.AddContractHandler(configEntryUpsertedSpec(), nil, "accesscore", "accesscore")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil handler")
}

func TestAddContractHandler_EmptyConsumerGroup_ReturnsError(t *testing.T) {
	t.Parallel()
	r := New(&blockingSubscriber{}, clock.Real())
	err := r.AddContractHandler(configEntryUpsertedSpec(), okHandler(), "", "test")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty consumerGroup")
}

func TestAddContractHandler_NonEventSpec_ReturnsError(t *testing.T) {
	t.Parallel()
	// Spec with Kind != "event" must be rejected at registration time.
	httpSpec := wrapper.ContractSpec{
		ID: "http.x.v1", Kind: "http", Transport: "http",
		Method: "POST", Path: "/x",
	}
	r := New(&blockingSubscriber{}, clock.Real())
	err := r.AddContractHandler(httpSpec, okHandler(), "mycell", "mycell")
	require.Error(t, err)
}

// --- Happy-path registration + tracing middleware ---

func TestAddContractHandler_RegistersBusinessHandler(t *testing.T) {
	t.Parallel()
	r := New(&blockingSubscriber{}, clock.Real())
	require.NoError(t, r.AddContractHandler(configEntryUpsertedSpec(), okHandler(), "accesscore", "accesscore"))
	assert.Equal(t, 1, r.HandlerCount())

	// Router stores the business handler; bootstrap-owned middleware wraps it.
	res := r.handlers[0].handler(context.Background(), outbox.Entry{})
	assert.Equal(t, outbox.DispositionAck, res.Disposition)
}

func TestContractTracingMiddleware_WrapsWithContractSpan(t *testing.T) {
	t.Parallel()
	tr := &contractSpyTracer{}
	r := New(&blockingSubscriber{}, clock.Real())

	var inner bool
	require.NoError(t, r.AddContractHandler(configEntryUpsertedSpec(), func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		inner = true
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	}, "accesscore", "accesscore"))
	require.Equal(t, 1, r.HandlerCount())

	// Drive one entry through the middleware position used by bootstrap:
	// contract tracing sits outside the stored business handler.
	sub := r.handlers[0].subscription()
	wrapped := ContractTracingMiddleware(tr, nil)(sub, r.handlers[0].handler)
	res := wrapped(context.Background(), outbox.Entry{EventType: "event.config.entry-upserted.v1"})
	assert.Equal(t, outbox.DispositionAck, res.Disposition)
	assert.True(t, inner, "inner handler must run")

	span := tr.only(t)
	attrs := span.attrMap()
	assert.Equal(t, "event.config.entry-upserted.v1", attrs["gocell.contract.id"], "gocell.contract.id")
	assert.Equal(t, "event", attrs["gocell.contract.kind"], "gocell.contract.kind")
	assert.Equal(t, "amqp", attrs["gocell.contract.transport"], "gocell.contract.transport")
	assert.Equal(t, "amqp", attrs["messaging.system"], "messaging.system")
	assert.Equal(t, "event.config.entry-upserted.v1", attrs["messaging.destination"], "messaging.destination")
	assert.Equal(t, "CONSUME event.config.entry-upserted.v1", span.name, "span name")
	assert.Equal(t, wrapper.StatusOK, span.status, "Ack must mark span StatusOK")
	assert.True(t, span.ended, "span.End() must have been called")
}

func TestContractTracingMiddleware_CoversDownstreamShortCircuit(t *testing.T) {
	t.Parallel()
	tr := &contractSpyTracer{}
	sub := outbox.Subscription{
		Topic:             "event.config.entry-upserted.v1",
		ConsumerGroup:     "accesscore",
		ContractID:        "event.config.entry-upserted.v1",
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
	res := wrapped(context.Background(), outbox.Entry{EventType: "event.config.entry-upserted.v1"})

	assert.Equal(t, outbox.DispositionRequeue, res.Disposition)
	assert.False(t, businessCalled, "downstream middleware should be allowed to skip business handler")

	span := tr.only(t)
	assert.Equal(t, wrapper.StatusError, span.status, "short-circuit Requeue must still mark the contract span")
	assert.True(t, span.ended, "span.End() must have been called")
	assert.Equal(t, "event.config.entry-upserted.v1", span.attrMap()["gocell.contract.id"])
}

// TestContractTracingMiddleware_PanicsOnEmptyContractID documents the F4
// fail-fast: once the ContractID==""  early-return was removed, a
// subscription that somehow reaches the middleware without a ContractID
// must panic via wrapper.WrapConsumer's spec.Validate() rather than
// silently skip tracing. Router.AddContractHandler prevents this today;
// this test is the backstop that catches any future regression.
func TestContractTracingMiddleware_PanicsOnEmptyContractID(t *testing.T) {
	t.Parallel()
	sub := outbox.Subscription{
		Topic:         "event.legacy.v1",
		ConsumerGroup: "legacy",
		// ContractID intentionally empty — simulates a legacy registration
		// path sneaking back in. wrapper.WrapConsumer.spec.Validate() must
		// panic at construction time so the regression is loud.
	}
	defer func() {
		r := recover()
		require.NotNil(t, r, "empty ContractID must panic at middleware construction")
	}()

	_ = ContractTracingMiddleware(wrapper.NoopTracer{}, nil)(sub,
		func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
			return outbox.HandleResult{Disposition: outbox.DispositionAck}
		})
}

func TestAddContractHandler_MultipleRegistrations_HandlersGrow(t *testing.T) {
	t.Parallel()
	r := New(&blockingSubscriber{}, clock.Real())
	for i := range 3 {
		spec := configEntryUpsertedSpec()
		spec.Topic = spec.Topic + "." + string(rune('a'+i))
		require.NoError(t, r.AddContractHandler(spec, okHandler(), "accesscore", "accesscore"))
	}
	assert.Equal(t, 3, r.HandlerCount())
}

// TestAddContractHandler_HandlerConfigShape verifies the wrapped handler is
// stored under spec.Topic (not a separate topic arg) and the consumerGroup
// is preserved.
func TestAddContractHandler_HandlerConfigShape(t *testing.T) {
	t.Parallel()
	r := New(&blockingSubscriber{}, clock.Real())
	require.NoError(t, r.AddContractHandler(configEntryUpsertedSpec(), okHandler(), "accesscore", "accesscore"))
	require.Equal(t, 1, len(r.handlers))
	cfg := r.handlers[0]
	assert.Equal(t, "event.config.entry-upserted.v1", cfg.topic, "topic derived from spec.Topic")
	assert.Equal(t, "accesscore", cfg.consumerGroup, "consumerGroup preserved")
	assert.NotNil(t, cfg.handler, "handler stored")
}

// TestAddContractHandler_OwnerCellIDDistinctFromConsumerGroup verifies that
// when ownerCellID differs from consumerGroup, Subscription.CellID takes the
// ownerCellID value (not consumerGroup).
//
// Concrete scenario: accesscore RBAC sync uses
//
//	consumerGroup = "accesscore-rbac-session-sync"
//	ownerCellID   = "accesscore"
//
// The Subscription.CellID must be "accesscore" for correct observability labels.
//
// ref: ThreeDotsLabs/watermill router.AddHandler handlerName / NATS subscription metadata.
func TestAddContractHandler_OwnerCellIDDistinctFromConsumerGroup(t *testing.T) {
	t.Parallel()
	r := New(&blockingSubscriber{}, clock.Real())
	const consumerGroup = "accesscore-rbac-session-sync"
	const ownerCellID = "accesscore"
	require.NoError(t, r.AddContractHandler(configEntryUpsertedSpec(), okHandler(), consumerGroup, ownerCellID))
	require.Equal(t, 1, len(r.handlers))
	sub := r.handlers[0].subscription()
	assert.Equal(t, consumerGroup, sub.ConsumerGroup, "ConsumerGroup must be preserved as-is")
	assert.Equal(t, ownerCellID, sub.CellID, "CellID must be ownerCellID, not consumerGroup")
}
