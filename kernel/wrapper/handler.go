package wrapper

import (
	"fmt"
	"net/http"

	"github.com/ghbvf/gocell/kernel/contractspec"
	"github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/panicregister"
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
// spec is validated at call time; invalid specs or nil handlers cause a
// non-nil error to be returned so the caller can choose between fail-fast
// (use MustHTTPHandler) and graceful refusal at composition time.
//
// ref: go-kratos/kratos middleware/tracing/tracing.go — the middleware is
// the single HTTP server span owner; handlers contribute attributes, not
// spans.
// ref: open-telemetry/opentelemetry-go-contrib otelhttp — "one middleware,
// one span" invariant; late-binding route metadata via chi RouteContext.
func HTTPHandler(spec contractspec.ContractSpec, next http.Handler) (http.Handler, error) {
	if err := validateHTTPHandlerArgs(spec, next); err != nil {
		return nil, err
	}
	baseAttrs := httpBaseAttrs(spec)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := ctxkeys.WithContractID(r.Context(), spec.ID)
		ctx = httputil.WithClientErrorLogSampling(ctx, spec.ID)
		if carrier, ok := AttrCarrierFrom(ctx); ok {
			carrier.Attrs = append(carrier.Attrs, baseAttrs...)
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	}), nil
}

// MustHTTPHandler is the composition-root fail-fast variant of HTTPHandler.
// It panics when HTTPHandler returns an error. Suitable for static wiring
// where the spec is a build-time literal; use HTTPHandler directly when the
// spec is data-driven.
func MustHTTPHandler(spec contractspec.ContractSpec, next http.Handler) http.Handler {
	h, err := HTTPHandler(spec, next)
	if err != nil {
		panic(panicregister.Approved("wrapper-handler-init", errcode.Assertion("wrapper: handler: %v", err)))
	}
	return h
}

func validateHTTPHandlerArgs(spec contractspec.ContractSpec, next http.Handler) error {
	if next == nil {
		return fmt.Errorf("wrapper.HTTPHandler: next handler must not be nil")
	}
	if spec.Kind != "http" {
		return fmt.Errorf("wrapper.HTTPHandler: spec.Kind %q must be \"http\"", spec.Kind)
	}
	if err := spec.Validate(); err != nil {
		return fmt.Errorf("wrapper.HTTPHandler: %w", err)
	}
	return nil
}

func httpBaseAttrs(spec contractspec.ContractSpec) []Attr {
	return []Attr{
		{Key: "gocell.contract.id", Value: spec.ID},
		{Key: "gocell.contract.kind", Value: string(spec.Kind)},
		{Key: "gocell.contract.transport", Value: spec.Transport},
		{Key: "http.method", Value: spec.Method},
		{Key: "http.route", Value: spec.Path},
	}
}
