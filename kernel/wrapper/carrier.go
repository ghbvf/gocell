package wrapper

import (
	"context"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
)

// AttrCarrier is a mutable holder for contract-derived span attributes. It
// is installed into the request context by the outer HTTP Tracing middleware
// before routing; HTTPHandler appends to it when the request reaches the
// contract-bound handler; Tracing reads the slice back after next.ServeHTTP
// returns and late-binds the attributes onto the single request-owned span.
//
// The carrier pattern mirrors chi's RouteContext: a pointer is stored in ctx
// once at the outermost layer, and nested handlers mutate its target. This
// is the canonical Go idiom when data must propagate "upwards" from a
// nested handler to an outer middleware span, since context.WithValue only
// adds new values to derived children.
//
// Rationale for putting the type + helpers in kernel/wrapper (not
// kernel/ctxkeys):
//   - the typed Attr slice is wrapper.Attr; keeping the carrier next to its
//     payload type avoids cycles in the opposite direction.
//   - kernel/wrapper already depends on kernel/ctxkeys for ContractID, so
//     the ctxkeys.ContractAttrs key can be referenced here directly.
//
// ref: go-chi/chi/v5 middleware/middleware.go — RouteContext's mutable
// routeContext struct in ctx, propagated up via chi-internal pointer.
type AttrCarrier struct {
	Attrs []Attr
}

// WithAttrCarrier returns a new context carrying c. A nil c is a no-op
// (returns ctx unchanged) so callers that do not need the carrier (tests,
// ad-hoc handler wiring without the outer HTTP Tracing middleware) need not
// guard at the call site.
func WithAttrCarrier(ctx context.Context, c *AttrCarrier) context.Context {
	if c == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxkeys.ContractAttrs, c)
}

// AttrCarrierFrom returns the attribute carrier from ctx. Reports false when
// the key is absent or holds nil — in which case callers skip attribute
// contribution (e.g. unit tests exercise HTTPHandler directly without the
// Tracing middleware chain, producing no span and no carrier).
func AttrCarrierFrom(ctx context.Context) (*AttrCarrier, bool) {
	v := ctx.Value(ctxkeys.ContractAttrs)
	if v == nil {
		return nil, false
	}
	c, ok := v.(*AttrCarrier)
	if !ok || c == nil {
		return nil, false
	}
	return c, true
}
