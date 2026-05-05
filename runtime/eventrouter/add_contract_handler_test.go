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

// --- Spy tracer for contract tracing verification ---

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
	r := New(wrap(&blockingSubscriber{}), clock.Real())
	err := r.AddContractHandler(configEntryUpsertedSpec(), nil, "accesscore", "accesscore")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil handler")
}

func TestAddContractHandler_EmptyConsumerGroup_ReturnsError(t *testing.T) {
	t.Parallel()
	r := New(wrap(&blockingSubscriber{}), clock.Real())
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
	r := New(wrap(&blockingSubscriber{}), clock.Real())
	err := r.AddContractHandler(httpSpec, okHandler(), "mycell", "mycell")
	require.Error(t, err)
}

// --- Happy-path registration + tracing middleware ---

func TestAddContractHandler_RegistersBusinessHandler(t *testing.T) {
	t.Parallel()
	r := New(wrap(&blockingSubscriber{}), clock.Real())
	require.NoError(t, r.AddContractHandler(configEntryUpsertedSpec(), okHandler(), "accesscore", "accesscore"))
	assert.Equal(t, 1, r.HandlerCount())

	// Router stores the business handler; bootstrap-owned middleware wraps it.
	res := r.handlers[0].handler(context.Background(), outbox.Entry{})
	assert.Equal(t, outbox.DispositionAck, res.Disposition)
}

func TestAddContractHandler_MultipleRegistrations_HandlersGrow(t *testing.T) {
	t.Parallel()
	r := New(wrap(&blockingSubscriber{}), clock.Real())
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
	r := New(wrap(&blockingSubscriber{}), clock.Real())
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
	r := New(wrap(&blockingSubscriber{}), clock.Real())
	const consumerGroup = "accesscore-rbac-session-sync"
	const ownerCellID = "accesscore"
	require.NoError(t, r.AddContractHandler(configEntryUpsertedSpec(), okHandler(), consumerGroup, ownerCellID))
	require.Equal(t, 1, len(r.handlers))
	sub := r.handlers[0].subscription()
	assert.Equal(t, consumerGroup, sub.ConsumerGroup, "ConsumerGroup must be preserved as-is")
	assert.Equal(t, ownerCellID, sub.CellID, "CellID must be ownerCellID, not consumerGroup")
}
