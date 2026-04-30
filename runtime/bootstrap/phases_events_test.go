package bootstrap

// phases_events_test.go — behavior tests for phase6StartEventRouter wiring.
//
// finding 2 (PR-A66 round-2) gaps these tests close:
//
//	1. WithConsumerMiddleware was previously unverified — the option set
//	   b.consumerMiddleware but no test proved that the middleware actually
//	   ended up in the subscription chain. A regression that drops the
//	   `mws = append(mws, b.consumerMiddleware...)` line in phases_events.go
//	   would have shipped silently.
//	2. WithEventRouterReadyTimeout was previously unverified — phase6 wires
//	   eventrouter.WithReadyTimeout(b.routerReadyTimeout) but no test
//	   exercised the timeout path. A regression that drops the option from
//	   evtRouterOpts would also have shipped silently.

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/runtime/eventbus"
	"github.com/ghbvf/gocell/runtime/http/health"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// stubEventCell is a minimal EventRegistrar that registers a single
// contract-first subscription. Used by the phase6 wiring tests below.
type stubEventCell struct {
	*cell.BaseCell
	spec wrapper.ContractSpec
}

func newStubEventCell(id, topic string) *stubEventCell {
	return &stubEventCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: id, Type: cell.CellTypeCore}),
		spec: wrapper.ContractSpec{
			ID:        topic,
			Kind:      "event",
			Transport: "inmem",
			Topic:     topic,
		},
	}
}

func (c *stubEventCell) RegisterSubscriptions(r cell.EventRouter) error {
	noopHandler := outbox.EntryHandler(func(_ context.Context, _ outbox.Entry) outbox.HandleResult {
		return outbox.HandleResult{Disposition: outbox.DispositionAck}
	})
	return r.AddContractHandler(c.spec, noopHandler, "stub-cg")
}

// TestPhase6_ConsumerMiddleware_AppliedInChain verifies that a middleware
// registered via WithConsumerMiddleware is actually invoked when the
// EventRouter wraps the per-topic handler. Pre-fix, this contract was
// untested; a regression in phases_events.go that dropped the append would
// have silently disabled every caller-supplied middleware.
func TestPhase6_ConsumerMiddleware_AppliedInChain(t *testing.T) {
	t.Parallel()

	bus := eventbus.New()

	var mwInvocations atomic.Int32
	spyMW := func(_ outbox.Subscription, next outbox.EntryHandler) outbox.EntryHandler {
		mwInvocations.Add(1) // counted at wrap time, once per Subscribe.
		return next
	}

	asm := assembly.New(assembly.Config{ID: "phase6-mw-test", DurabilityMode: cell.DurabilityDemo})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newStubEventCell("stub", "event.phase6.mw.v1")))
	require.NoError(t, asm.Start(context.Background()))

	b := New(
		WithAssembly(asm),
		WithPublisher(bus),
		WithSubscriber(bus),
		WithConsumerMiddleware(spyMW),
	)

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.sub = bus
	s.hh = health.New(asm) // phase5 normally populates this; test bypasses phase5.

	require.NoError(t, b.phase6StartEventRouter(runCtx, s),
		"phase6 must start cleanly with one stub subscription")

	assert.Equal(t, int32(1), mwInvocations.Load(),
		"WithConsumerMiddleware must be applied to the subscription chain exactly once for one subscription")

	// Run all teardowns to release the eventrouter goroutine before the test exits.
	for _, v := range slices.Backward(s.teardowns) {
		_ = v.fn(context.Background())
	}
}

// neverReadySubscriber is a Subscriber that completes Setup but never closes
// the Ready channel, simulating a broker subscription that hangs in the
// "not ready" state. Subscribe blocks until ctx is canceled.
type neverReadySubscriber struct{}

func (neverReadySubscriber) Setup(_ context.Context, _ outbox.Subscription) error {
	return nil
}

func (neverReadySubscriber) Ready(_ outbox.Subscription) <-chan struct{} {
	// Returning a never-closed channel keeps the eventrouter waiting.
	return make(chan struct{})
}

func (neverReadySubscriber) Subscribe(ctx context.Context, _ outbox.Subscription, _ outbox.EntryHandler) error {
	<-ctx.Done()
	return ctx.Err()
}

func (neverReadySubscriber) Close(_ context.Context) error { return nil }

// TestPhase6_EventRouterReadyTimeout_FiresAndReturnsError verifies that
// WithEventRouterReadyTimeout actually plumbs through to
// eventrouter.WithReadyTimeout: a subscription that never reports ready
// must trigger a timeout error from phase6 within the configured budget.
func TestPhase6_EventRouterReadyTimeout_FiresAndReturnsError(t *testing.T) {
	t.Parallel()

	asm := assembly.New(assembly.Config{ID: "phase6-rt-test", DurabilityMode: cell.DurabilityDemo})
	t.Cleanup(asm.Shutdown)
	require.NoError(t, asm.Register(newStubEventCell("stub", "event.phase6.rt.v1")))
	require.NoError(t, asm.Start(context.Background()))

	b := New(
		WithAssembly(asm),
		WithSubscriber(neverReadySubscriber{}),
		WithEventRouterReadyTimeout(80*time.Millisecond),
	)

	runCtx, s := newPhaseState()
	defer s.runCancel()
	s.asm = asm
	s.sub = neverReadySubscriber{}
	s.hh = health.New(asm)

	start := time.Now()
	err := b.phase6StartEventRouter(runCtx, s)
	elapsed := time.Since(start)

	require.Error(t, err, "phase6 must surface the ready-timeout error")
	assert.Contains(t, err.Error(), "not ready",
		"error message must identify the not-ready failure mode")
	// Lower bound proves the timeout fires; upper bound proves the budget is honored
	// rather than blocking indefinitely. Generous upper bound to keep the test stable
	// on slow CI without weakening the contract.
	assert.GreaterOrEqual(t, elapsed, 80*time.Millisecond,
		"timeout must wait at least the configured budget")
	assert.Less(t, elapsed, 2*time.Second,
		"timeout must not exceed budget by more than 25x — the option clearly is not plumbed")
}
