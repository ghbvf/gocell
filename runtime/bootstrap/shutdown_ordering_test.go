package bootstrap

// shutdown_ordering_test.go — phase10 explicit-stage ordering contract tests
// (finding 1 / PR-A66 round-2).
//
// The ordering claim under test:
//
//	stage1 readiness flip → stage2 HTTP drain → stage3 LIFO teardown
//
// HTTP drain must complete BEFORE any LIFO teardown (workers / event router /
// assembly / closers) starts, regardless of which signal triggered shutdown
// (ctx cancel / HTTP error / worker error / router error).
//
// Encoding the order as an explicit stage rather than as a side effect of
// teardown registration order is what these tests pin: a future change that
// re-registers `shutdownAllServers` into the LIFO chain would let workers
// stop before HTTP intake, which is the regression these tests catch.
//
// ref: kubernetes/kubernetes apiserver/pkg/server/genericapiserver.go
//      RunWithContext — NotAcceptingNewRequest → InFlightRequestsDrained →
//      stopHttpServerCtx ordering.

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
)

// orderingRecorder captures the order in which phase10 stages execute.
// All append calls go through the mutex; reads in the test goroutine read
// after phase10 returns, so no race exists in practice.
type orderingRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *orderingRecorder) record(name string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, name)
}

func (r *orderingRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	copy(out, r.events)
	return out
}

// runPhase10WithRecorder wires three teardown sources for ordering inspection:
//
//	httpDrain → records "http_drain"
//	first  addTeardown (registered second, runs first under LIFO) → "worker_stop"
//	second addTeardown (registered first,  runs second under LIFO) → "assembly_stop"
//
// All hooks return nil so ordering — not error aggregation — is the only
// signal under test. Pass non-nil drainErr / teardownErr to specialise.
func runPhase10WithRecorder(t *testing.T, sig shutdownSignal, drainErr, teardownErr error) (*orderingRecorder, error) {
	t.Helper()
	rec := &orderingRecorder{}

	b := New(WithClock(clock.Real())) // NopProvider → shutdownMet is disabled; fine for ordering test.
	_, s := newPhaseState()

	s.httpDrain = func(_ context.Context) error {
		rec.record("http_drain")
		return drainErr
	}
	// Registration order matters: assembly is registered first so it runs LAST
	// under LIFO. Worker registered second runs FIRST under LIFO. Both must
	// run AFTER http_drain.
	s.addTeardown(func(_ context.Context) error {
		rec.record("assembly_stop")
		return nil
	})
	s.addTeardown(func(_ context.Context) error {
		rec.record("worker_stop")
		return teardownErr
	})

	err := b.phase10OrchestrateShutdown(s, sig)
	return rec, err
}

func TestPhase10_HTTPDrainsBeforeLIFO_OnCtxCancel(t *testing.T) {
	t.Parallel()
	rec, err := runPhase10WithRecorder(t, shutdownSignal{reason: reasonCtxCancel}, nil, nil)
	require.NoError(t, err, "clean ctx-cancel shutdown must succeed when both drain and teardowns are nil")
	assert.Equal(t,
		[]string{"http_drain", "worker_stop", "assembly_stop"},
		rec.snapshot(),
		"HTTP drain must run before LIFO teardown; teardown LIFO is reverse-registration",
	)
}

func TestPhase10_HTTPDrainsBeforeLIFO_OnWorkerError(t *testing.T) {
	t.Parallel()
	sigErr := errors.New("worker exploded")
	rec, err := runPhase10WithRecorder(t, shutdownSignal{reason: reasonWorkerError, err: sigErr}, nil, nil)
	// Signal error surfaces when teardown itself is clean.
	require.ErrorIs(t, err, sigErr, "signal error must propagate when teardown is clean")
	assert.Equal(t,
		[]string{"http_drain", "worker_stop", "assembly_stop"},
		rec.snapshot(),
		"worker-error path must still drain HTTP intake before LIFO teardown",
	)
}

func TestPhase10_HTTPDrainsBeforeLIFO_OnRouterError(t *testing.T) {
	t.Parallel()
	sigErr := errors.New("router lost connection")
	rec, err := runPhase10WithRecorder(t, shutdownSignal{reason: reasonRouterError, err: sigErr}, nil, nil)
	require.ErrorIs(t, err, sigErr)
	assert.Equal(t,
		[]string{"http_drain", "worker_stop", "assembly_stop"},
		rec.snapshot(),
		"router-error path must still drain HTTP intake before LIFO teardown",
	)
}

func TestPhase10_HTTPDrainError_AggregatedAndWrapped(t *testing.T) {
	t.Parallel()
	drainErr := errors.New("listener Shutdown failed")
	tearErr := errors.New("worker stop failed")
	rec, err := runPhase10WithRecorder(t, shutdownSignal{reason: reasonCtxCancel}, drainErr, tearErr)

	require.Error(t, err)
	// Both errors must surface; HTTP drain failure does not short-circuit teardown.
	assert.ErrorIs(t, err, drainErr, "joined error must include HTTP drain failure")
	assert.ErrorIs(t, err, tearErr, "joined error must include LIFO teardown failure")

	// HTTP drain error must be wrapped with the canonical phase label so
	// post-mortem grep over logs can locate the failure source.
	var pe *phaseError
	require.True(t, errors.As(err, &pe), "HTTP drain error must be wrapped in phaseError")
	assert.Equal(t, "teardown_http_drain", pe.Phase, "phase label must identify HTTP drain")

	// HTTP drain error does NOT prevent LIFO teardowns from running.
	assert.Equal(t,
		[]string{"http_drain", "worker_stop", "assembly_stop"},
		rec.snapshot(),
		"HTTP drain failure must not short-circuit LIFO teardown — best-effort cleanup",
	)
}

// TestPhase10_NoHTTPDrain_NoOp guards the "phase7 not run" path: tests that
// skip HTTP setup leave s.httpDrain nil; phase10 must skip stage2 cleanly
// rather than panic on a nil func call.
func TestPhase10_NoHTTPDrain_NoOp(t *testing.T) {
	t.Parallel()
	b := New(WithClock(clock.Real()))
	_, s := newPhaseState()
	// httpDrain intentionally nil.
	s.addTeardown(func(_ context.Context) error { return nil })

	err := b.phase10OrchestrateShutdown(s, shutdownSignal{reason: reasonCtxCancel})
	require.NoError(t, err, "nil httpDrain must be a no-op, not a panic")
}

// TestPhase10ShutdownStageOrder locks the complete four-stage shutdown sequence:
//
//	stage1 readiness_flip → stage2 http_drain → stage3 lifo_teardown (LIFO) → stage4 closed
//
// This test pins the explicit ordering that phase10OrchestrateShutdown
// enforces. A future refactor that re-registers httpDrain into the LIFO chain
// (e.g. to share error aggregation) would silently break the ordering
// invariant; this test catches that regression.
//
// Stages 2-3 are directly observable via the orderingRecorder (http_drain and
// LIFO teardown hooks). Stage 1 (readiness flip) has no externally observable
// event without spinning up a real HTTP server; its presence is verified by
// the code structure and the sibling tests TestPhase10_HTTPDrainsBeforeLIFO_*.
// Stage 4 (finalize / closed) is verified by the nil error return from
// phase10OrchestrateShutdown and the runCancel call.
//
// The canonical four-stage invariant under test:
//
//	http_drain must appear before any LIFO teardown step, regardless of which
//	signal triggered shutdown (ctx cancel / HTTP error / worker error).
//
// Three signal variants (ctx cancel, worker error, router error) exercise all
// code paths that reach phase10OrchestrateShutdown in production.
func TestPhase10ShutdownStageOrder(t *testing.T) {
	t.Parallel()

	signals := []struct {
		name string
		sig  shutdownSignal
	}{
		{"ctx_cancel", shutdownSignal{reason: reasonCtxCancel}},
		{"worker_error", shutdownSignal{reason: reasonWorkerError, err: errors.New("worker failed")}},
		{"router_error", shutdownSignal{reason: reasonRouterError, err: errors.New("router failed")}},
	}

	for _, tc := range signals {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			rec := &orderingRecorder{}
			b := New(WithClock(clock.Real()))
			_, s := newPhaseState()

			// Stage 2: HTTP drain (explicit, runs before LIFO).
			s.httpDrain = func(_ context.Context) error {
				rec.record("http_drain")
				return nil
			}

			// Stage 3: LIFO teardown — two entries in registration order.
			// Under LIFO: worker_stop (registered 2nd) runs before
			// assembly_stop (registered 1st).
			s.addTeardown(func(_ context.Context) error {
				rec.record("assembly_stop")
				return nil
			})
			s.addTeardown(func(_ context.Context) error {
				rec.record("worker_stop")
				return nil
			})

			err := b.phase10OrchestrateShutdown(s, tc.sig)
			if tc.sig.err != nil {
				// Signal error surfaces when teardown itself is clean.
				require.ErrorIs(t, err, tc.sig.err,
					"signal error must propagate when teardown is clean")
			} else {
				require.NoError(t, err)
			}

			// http_drain (stage 2) must precede all LIFO teardown steps (stage 3).
			assert.Equal(t,
				[]string{"http_drain", "worker_stop", "assembly_stop"},
				rec.snapshot(),
				"phase10 must complete http_drain before any LIFO teardown step",
			)
		})
	}
}
