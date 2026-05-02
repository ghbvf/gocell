//go:build integration

package bootstrap

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// F3 round-4 integration test: verifies the end-to-end path
// bootstrap.WithTracer -> ContractTracingMiddleware -> wrapper.WrapConsumer ->
// span.Start on a real subscribed event. A regression at any layer would
// silently degrade consumer-side observability; this test is the only
// cross-layer check that proves the wire stays connected.

// endToEndSpyTracer records every tracer.Start invocation so the test can
// assert on the full (name, attrs) tuple after the event bus round-trips.
type endToEndSpyTracer struct {
	mu    sync.Mutex
	spans []*endToEndSpySpan
}

func (t *endToEndSpyTracer) Start(ctx context.Context, name string, _ ...wrapper.Attr) (context.Context, wrapper.Span) {
	s := &endToEndSpySpan{name: name}
	t.mu.Lock()
	t.spans = append(t.spans, s)
	t.mu.Unlock()
	return ctx, s
}

func (t *endToEndSpyTracer) consumeSpanWithName(prefix string) (*endToEndSpySpan, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, s := range t.spans {
		if len(s.name) >= len(prefix) && s.name[:len(prefix)] == prefix {
			return s, true
		}
	}
	return nil, false
}

type endToEndSpySpan struct {
	mu     sync.Mutex
	name   string
	attrs  []wrapper.Attr
	status wrapper.StatusCode
	ended  bool
}

func (s *endToEndSpySpan) SetAttributes(a ...wrapper.Attr) {
	s.mu.Lock()
	s.attrs = append(s.attrs, a...)
	s.mu.Unlock()
}
func (s *endToEndSpySpan) RecordError(_ error) {}
func (s *endToEndSpySpan) SetStatus(c wrapper.StatusCode, _ string) {
	s.mu.Lock()
	s.status = c
	s.mu.Unlock()
}
func (s *endToEndSpySpan) End() { s.mu.Lock(); s.ended = true; s.mu.Unlock() }

func (s *endToEndSpySpan) attrMap() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]any, len(s.attrs))
	for _, a := range s.attrs {
		out[a.Key] = a.Value
	}
	return out
}

// consumerSpyCell registers a single contract-first subscription and records
// every invocation of the inner handler so the test can assert message
// delivery round-trip in addition to the span assertions.
type consumerSpyCell struct {
	cell.BaseCell
	spec  wrapper.ContractSpec
	calls chan outbox.Entry
}

func (c *consumerSpyCell) RegisterSubscriptions(r cell.EventRouter) error {
	handler := outbox.EntryHandler(func(_ context.Context, entry outbox.Entry) outbox.HandleResult {
		c.calls <- entry
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})
	_ = r.AddContractHandler(c.spec, handler, "consumer-spy")
	return nil
}

// TestBootstrap_ConsumerTracingIntegration wires bootstrap ->
// AddContractHandler -> ContractTracingMiddleware -> WrapConsumer in the most
// production-like way available without a real broker, then asserts that
// delivering one event produces exactly one CONSUME span tagged with the
// contract metadata.
func TestBootstrap_ConsumerTracingIntegration(t *testing.T) {
	spyTracer := &endToEndSpyTracer{}
	bus := eventbus.New(eventbus.WithClock(clock.Real()))

	spec := wrapper.ContractSpec{
		ID: "event.integration.test.v1", Kind: "event", Transport: "inmem",
		Topic: "event.integration.test.v1",
	}
	cellImpl := &consumerSpyCell{
		BaseCell: *cell.NewBaseCell(cell.CellMetadata{ID: "consumerspy"}),
		spec:     spec,
		calls:    make(chan outbox.Entry, 1),
	}

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	asm := assembly.New(assembly.Config{ID: "consumer-trace-int", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Register(cellImpl))

	primaryLn := newLocalListener(t)
	b := New(
		WithClock(clock.Real()),
		WithSubscriber(bus),
		WithPublisher(bus),
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
		WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(newLocalListener(t))),
		WithTracer(spyTracer),
	)

	runErr := make(chan error, 1)
	go func() { runErr <- b.Run(ctx) }()

	// Wait until Bootstrap reports the primary listener healthy — by then
	// phase6StartEventRouter has run and the subscription is live.
	addr := primaryLn.Addr().String()
	waitForHealthy(t, addr)

	// Publish one entry on the topic and await handler invocation.
	// The bus requires a v1 wire envelope, built via outbox.MarshalEnvelope.
	bodyPayload, _ := json.Marshal(map[string]string{"hello": "world"})
	envelope, err := outbox.MarshalEnvelope(outbox.Entry{
		ID:        "integration-evt-1",
		EventType: "integration.test",
		Topic:     spec.Topic,
		Payload:   bodyPayload,
		CreatedAt: time.Now(),
	})
	require.NoError(t, err)
	err = bus.Publish(ctx, spec.Topic, envelope)
	require.NoError(t, err)

	select {
	case got := <-cellImpl.calls:
		assert.Equal(t, "integration-evt-1", got.ID)
	case <-time.After(testtime.D2s):
		t.Fatal("consumer handler was never invoked within 2s")
	}

	// Shut down cleanly before asserting on the span so the
	// WrapConsumer-installed span.End has definitely run.
	cancel()
	<-runErr

	span, ok := spyTracer.consumeSpanWithName("CONSUME ")
	require.True(t, ok, "expected a CONSUME span; got none")
	assert.True(t, span.ended, "span must be ended after handler returns")
	assert.Equal(t, wrapper.StatusOK, span.status, "Ack disposition must mark span StatusOK")

	attrs := span.attrMap()
	assert.Equal(t, "event.integration.test.v1", attrs["gocell.contract.id"])
	assert.Equal(t, "event.integration.test.v1", attrs["messaging.destination"])
	assert.Equal(t, "inmem", attrs["messaging.system"])
}
