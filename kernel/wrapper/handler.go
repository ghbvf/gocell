package wrapper

import (
	"fmt"
	"net/http"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
)

// HTTPHandler wraps next with contract-id propagation and contract-derived
// span attribute contribution. It does NOT create its own trace span —
// the outer runtime/http/middleware.Tracing middleware owns the single
// request-owned span per the round-4 single-owner design. HTTPHandler's
// job is purely ctx-plumbing:
//
//  1. write ctxkeys.ContractID so slog + downstream handlers can read it
//  2. append contract base attributes (gocell.contract.id / kind / transport
//     + http.method / http.route) to the AttrCarrier installed by the outer
//     Tracing middleware; after next.ServeHTTP returns the middleware
//     late-binds the collected attributes onto its span
//
// When no AttrCarrier is present (unit tests that exercise HTTPHandler
// standalone, ad-hoc wiring without the outer Tracing chain), the
// attribute contribution is silently skipped — there is no span to
// decorate anyway, and ContractID is still written for slog + handler
// consumption.
//
// Panic recovery: HTTPHandler does NOT install its own recover(). Panics
// propagate up to runtime/http/middleware.Recovery (error response) and
// runtime/http/middleware.Tracing (span status = error + RecordError on
// the outer span).
//
// spec is validated at call time; invalid specs or nil handlers panic
// (fail-fast at registration time beats a silent miss at request time).
//
// ref: go-kratos/kratos middleware/tracing/tracing.go — the middleware is
// the single HTTP server span owner; handlers contribute attributes, not
// spans.
// ref: open-telemetry/opentelemetry-go-contrib otelhttp — "one middleware,
// one span" invariant; late-binding route metadata via chi RouteContext.
func HTTPHandler(spec ContractSpec, next http.Handler) http.Handler {
	validateHTTPHandlerArgs(spec, next)
	baseAttrs := httpBaseAttrs(spec)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := ctxkeys.WithContractID(r.Context(), spec.ID)
		if carrier, ok := AttrCarrierFrom(ctx); ok {
			carrier.Attrs = append(carrier.Attrs, baseAttrs...)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func validateHTTPHandlerArgs(spec ContractSpec, next http.Handler) {
	if next == nil {
		panic("wrapper.HTTPHandler: next handler must not be nil")
	}
	if spec.Kind != "http" {
		panic(fmt.Sprintf("wrapper.HTTPHandler: spec.Kind %q must be \"http\"", spec.Kind))
	}
	if err := spec.Validate(); err != nil {
		panic(err.Error())
	}
}

func httpBaseAttrs(spec ContractSpec) []Attr {
	return []Attr{
		{Key: "gocell.contract.id", Value: spec.ID},
		{Key: "gocell.contract.kind", Value: spec.Kind},
		{Key: "gocell.contract.transport", Value: spec.Transport},
		{Key: "http.method", Value: spec.Method},
		{Key: "http.route", Value: spec.Path},
	}
}
