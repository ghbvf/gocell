package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// spanName is the trace span name for an in-process contract dispatch.
const spanName = "cell_transport.in_proc"

// msgTransportNotBound is the const-literal fail-fast message for a DoContract
// call that arrives before the built handler is bound (MESSAGE-CONST-LITERAL-01).
const msgTransportNotBound = "in-process transport: DoContract called before the internal-listener handler was bound (bootstrap phase5)"

// InProcessTransport is the co-located implementation of [CellTransport]: it
// dispatches a contract request in memory against the BUILT internal-listener
// http.Handler, replacing the loopback TCP hop with a direct ServeHTTP. It does
// NOT bypass the listener auth chain — the dispatched request runs the same
// ServiceTokenMiddleware + RequireCallerCell as a remote call (ADR D4).
//
// Sealed (Hard): all fields are unexported and the sole constructor is
// [NewInProcess], so an external package cannot forge a transport or set the
// handler by struct literal. The field set is frozen by INPROCESS-TRANSPORT-SEALED-01.
//
// Lifecycle (WriteOnce late-bind): the composition root constructs the holder
// EMPTY (the handler does not exist until bootstrap phase5) and shares it by
// reference with both the consumer cell (via composition.SharedDeps) and
// bootstrap. [InProcessTransport.Bind] binds the finalized handler exactly once
// at phase5, before serving. A DoContract before Bind fails fast.
type InProcessTransport struct {
	// bound is set true by Bind's CompareAndSwap; it is the release/acquire
	// barrier publishing handler+tracer to concurrent DoContract readers.
	bound atomic.Bool
	// handler is the built internal-listener handler; written once under Bind's
	// CAS, read after bound.Load() observes true.
	handler http.Handler
	// tracer creates the per-dispatch span; NoopTracer when none is wired.
	tracer wrapper.Tracer
	// metrics records transport_mode; may be nil (records nothing).
	metrics *Metrics
}

// NewInProcess constructs an EMPTY in-process transport holder. The built
// internal-listener handler is bound later via [InProcessTransport.Bind] at
// bootstrap phase5. metrics may be nil (no metric recording); production wires a
// real *Metrics from the composition root's metrics provider.
func NewInProcess(metrics *Metrics) *InProcessTransport {
	return &InProcessTransport{metrics: metrics}
}

// Bind attaches the built internal-listener handler (and tracer) to the
// transport, exactly once (WriteOnce). It is called by bootstrap phase5 after
// the internal router is built and auth-finalized, so DoContract dispatches see
// the same compiled auth chain as a network request. A nil tracer degrades to
// [wrapper.NoopTracer]. A second Bind returns an error (the CompareAndSwap loses).
func (t *InProcessTransport) Bind(handler http.Handler, tracer wrapper.Tracer) error {
	if handler == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"in-process transport: Bind called with a nil handler")
	}
	if tracer == nil {
		tracer = wrapper.NoopTracer{}
	}
	t.handler = handler
	t.tracer = tracer
	if !t.bound.CompareAndSwap(false, true) {
		return errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"in-process transport: Bind called more than once (WriteOnce)")
	}
	return nil
}

// DoContract dispatches req in memory against the bound internal-listener
// handler and returns the reconstructed response. The ctx is applied to the
// request so cancellation / values reach the handler. The full listener
// middleware chain (incl. ServiceTokenMiddleware + RequireCallerCell) runs — the
// in-process path replaces the network, not the governance stack (ADR D4).
//
// Before Bind it fails fast with KindInternal (a misordered-wiring programmer
// error), never a nil-handler panic.
func (t *InProcessTransport) DoContract(ctx context.Context, contractID string, req *http.Request) (*http.Response, error) {
	if !t.bound.Load() {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrInternal, msgTransportNotBound,
			errcode.WithInternal(errcode.InternalAttr("contractID", contractID)))
	}

	ctx, span := t.tracer.Start(ctx, spanName,
		wrapper.Attr{Key: labelTransportMode, Value: modeInProc.String()},
		wrapper.Attr{Key: "contract.id", Value: contractID})
	defer span.End()

	rec := httptest.NewRecorder()
	t.handler.ServeHTTP(rec, req.WithContext(ctx))
	t.metrics.Record(ctx, modeInProc)

	return rec.Result(), nil
}

// Compile-time: InProcessTransport satisfies the seam.
var _ CellTransport = (*InProcessTransport)(nil)
