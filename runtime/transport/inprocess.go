package transport

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"

	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// spanName is the trace span name for a cross-cell contract dispatch. Mode-neutral
// (transport_mode is a span attribute / metric label, not baked into the name) so
// the future remote transport (US5 #1966) shares one span name.
const spanName = "cell_transport.dispatch"

// attrHTTPStatusCode is the span attribute key for the dispatched response status.
const attrHTTPStatusCode = "http.status_code"

// msgTransportNotBound is the const-literal fail-fast message for a DoContract
// call that arrives before the built handler is bound (MESSAGE-CONST-LITERAL-01).
const msgTransportNotBound = "in-process transport: DoContract called before the internal-listener handler was bound (bootstrap phase5)"

// boundState is the immutable handler+tracer pair published atomically by Bind.
// Holding both in one pointer lets a single CompareAndSwap publish the whole
// state — no plain field writes race with a concurrent DoContract reader.
type boundState struct {
	handler http.Handler
	tracer  wrapper.Tracer
}

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
// bootstrap. [InProcessTransport.Bind] publishes the finalized handler exactly
// once at phase5 (single atomic CompareAndSwap — race-free), before serving. A
// DoContract before Bind fails fast.
type InProcessTransport struct {
	// state is nil until Bind publishes the handler+tracer with one atomic CAS;
	// DoContract reads it with an acquire Load, seeing either nil (fail-fast) or
	// the fully-published state — never a torn write.
	state atomic.Pointer[boundState]
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

// Bind atomically publishes the built internal-listener handler (and tracer) to
// the transport, exactly once (WriteOnce). It is called by bootstrap phase5 after
// the internal router is built and auth-finalized, so DoContract dispatches see
// the same compiled auth chain as a network request. A nil tracer degrades to
// [wrapper.NoopTracer]. A nil handler is a programmer error (fail-fast). A second
// Bind returns an error (the CompareAndSwap loses).
func (t *InProcessTransport) Bind(handler http.Handler, tracer wrapper.Tracer) error {
	if handler == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrInternal,
			"in-process transport: Bind called with a nil handler")
	}
	if tracer == nil {
		tracer = wrapper.NoopTracer{}
	}
	if !t.state.CompareAndSwap(nil, &boundState{handler: handler, tracer: tracer}) {
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
// error), never a nil-handler panic. The dispatch span carries transport_mode +
// the response status, and is marked StatusError on a 5xx so an in-proc call is
// never silently "successful" in traces (ADR D4: transparent ≠ undiagnosable).
func (t *InProcessTransport) DoContract(ctx context.Context, contractID string, req *http.Request) (*http.Response, error) {
	bs := t.state.Load()
	if bs == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrInternal, msgTransportNotBound,
			errcode.WithInternal(errcode.InternalAttr("contractID", contractID)))
	}

	ctx, span := bs.tracer.Start(ctx, spanName,
		wrapper.Attr{Key: labelTransportMode, Value: modeInProc.String()},
		wrapper.Attr{Key: "contract.id", Value: contractID})
	defer span.End()

	rec := httptest.NewRecorder()
	bs.handler.ServeHTTP(rec, req.WithContext(ctx))
	t.metrics.Record(ctx, modeInProc)

	span.SetAttributes(wrapper.Attr{Key: attrHTTPStatusCode, Value: int64(rec.Code)})
	if rec.Code >= http.StatusInternalServerError {
		span.SetStatus(wrapper.StatusError, http.StatusText(rec.Code))
	}

	return rec.Result(), nil
}

// Compile-time: InProcessTransport satisfies the seam.
var _ CellTransport = (*InProcessTransport)(nil)
