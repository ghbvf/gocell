package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// TracingOption configures the Tracing middleware.
type TracingOption func(*tracingConfig)

type tracingConfig struct {
	publicEndpointFn func(*http.Request) bool
	skipFn           func(*http.Request) bool
	errorRedactor    wrapper.ErrorRedactor
}

// WithPublicEndpointFn sets a per-request function that determines whether an
// endpoint is public-facing (untrusted upstream). When it returns true, the
// middleware creates a new root trace instead of continuing the upstream trace.
// The remote span context is recorded as linked attributes for correlation.
//
// ref: otelhttp WithPublicEndpointFn — new root + trace.Link to remote context
func WithPublicEndpointFn(fn func(*http.Request) bool) TracingOption {
	return func(c *tracingConfig) { c.publicEndpointFn = fn }
}

// WithProbeFilter installs a predicate that, when true for a given request,
// bypasses outer span creation entirely (the request is forwarded to next
// without a span). Use for health/readiness/liveness probe paths and other
// high-rate infra traffic where span volume is cost-prohibitive.
//
// Combine with DefaultProbeFilter to skip the canonical probe endpoints
// (/healthz, /readyz, /livez, /metrics) that the Router registers directly
// on the outer mux.
//
// ref: open-telemetry/opentelemetry-go-contrib otelhttp/config.go — Filter type.
func WithProbeFilter(pred func(*http.Request) bool) TracingOption {
	return func(c *tracingConfig) {
		if pred != nil {
			c.skipFn = pred
		}
	}
}

// WithErrorRedactor installs a redactor for errors recorded on the active
// HTTP span. Recovery uses the recorder installed by Tracing to attach panic
// errors to the span without leaking raw panic text into the tracing backend.
func WithErrorRedactor(fn wrapper.ErrorRedactor) TracingOption {
	return func(c *tracingConfig) {
		if fn != nil {
			c.errorRedactor = fn
		}
	}
}

// DefaultProbeFilter skips the canonical infra probe endpoints. Matched
// paths (exact): /healthz, /readyz, /livez, /metrics. Router.buildOuterMux
// applies it by default so high-frequency probe traffic does not emit spans.
func DefaultProbeFilter(r *http.Request) bool {
	switch r.URL.Path {
	case "/healthz", "/readyz", "/livez", "/metrics":
		return true
	default:
		return false
	}
}

// Tracing creates an HTTP middleware that starts a span for each request.
// The span is initially named "{method} {path}" and renamed to
// "{method} {routePattern}" after routing completes (if the span supports
// SpanRenamer). The http.route attribute carries the low-cardinality route
// pattern for OTel semantic conventions compliance.
//
// Span status follows the otelhttp convention: 5xx responses mark the span
// as error with the status text as description; 4xx and below leave the
// span status unset (the status code attribute is always recorded).
//
// ref: otelchi — extracts chi RoutePattern for span name after routing
// ref: otelhttp handler.go — span status set for 5xx, unset for 4xx
// ref: OTel semantic conventions — http.route must be low-cardinality template
//
// When inbound headers carry tracing context, Tracing continues the upstream
// trace before span creation. W3C `traceparent` takes precedence and B3 is
// used only as a fallback. Invalid headers are ignored and result in a new
// root span.
//
// For public-facing endpoints (determined by WithPublicEndpointFn), inbound
// trace context is NOT inherited. A new root trace is created and the remote
// context is recorded as linked attributes (linked.trace_id, linked.span_id).
//
// When a RecorderState exists in the context (created by the Recorder
// middleware), Tracing reuses it. Otherwise it creates its own to capture
// http.status_code as a standalone middleware.
//
// Single-span ownership (round-4): Tracing is the single HTTP request span
// owner. kernel/wrapper.HTTPHandler no longer creates an inner span — it
// writes contract id + contract-derived attributes (gocell.contract.id /
// kind / transport) into a shared AttrCarrier that Tracing installs in ctx
// before calling next.ServeHTTP; after next returns, Tracing late-binds the
// collected attributes onto its span. This means every contract-bound
// request produces exactly one server span annotated with both http.route
// and gocell.contract.* attributes — no duplicate counting in dashboards.
//
// Probe bypass: when skipFn (via WithProbeFilter) returns true, Tracing
// skips span creation entirely. This replaces the earlier wrapper-level
// DefaultProbeFilter that never fired in practice because probe routes are
// registered on the outer mux and bypass wrapper.HTTPHandler entirely.
func Tracing(tracer tracing.Tracer, opts ...TracingOption) func(http.Handler) http.Handler {
	var cfg tracingConfig
	for _, o := range opts {
		o(&cfg)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if cfg.skipFn != nil && cfg.skipFn(r) {
				next.ServeHTTP(w, r)
				return
			}
			serveSpanned(tracer, cfg, next, w, r)
		})
	}
}

// serveSpanned starts the outer request span, delegates to next, then
// finalises the span. Extracted to keep Tracing's cognitive complexity ≤ 15.
func serveSpanned(tracer tracing.Tracer, cfg tracingConfig, next http.Handler, w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	isPublic := cfg.publicEndpointFn != nil && cfg.publicEndpointFn(r)

	// Extract remote span context for linking (before deciding to use it).
	extractedCtx := extractTraceContext(ctx, r.Header)
	remoteSpanCtx := oteltrace.SpanContextFromContext(extractedCtx)

	if !isPublic {
		// Trusted upstream: continue the upstream trace.
		ctx = extractedCtx
	}
	// Public endpoint: ctx stays clean → tracer creates new root.

	// Install an AttrCarrier so wrapper.HTTPHandler (if the request reaches
	// a contract-bound route) can append gocell.contract.* attrs. Tracing
	// drains the carrier after next.ServeHTTP returns.
	carrier := &wrapper.AttrCarrier{}
	ctx = wrapper.WithAttrCarrier(ctx, carrier)

	// Start span with tentative name using raw path.
	// After routing, the span is renamed to use the route pattern.
	ctx, span := tracer.Start(ctx, r.Method+" "+r.URL.Path)
	ctx = withSpanErrorRecorder(ctx, span, cfg.errorRedactor)
	defer span.End()

	// Record linked remote context for public endpoints.
	if isPublic && remoteSpanCtx.IsValid() && remoteSpanCtx.IsRemote() {
		span.SetAttributes(
			tracing.Attr{Key: "linked.trace_id", Value: remoteSpanCtx.TraceID().String()},
			tracing.Attr{Key: "linked.span_id", Value: remoteSpanCtx.SpanID().String()},
		)
	}

	state := RecorderStateFrom(ctx)
	if state == nil {
		var wrapped http.ResponseWriter
		state, wrapped = NewRecorder(w)
		w = wrapped
		ctx = WithRecorderState(ctx, state)
	}

	next.ServeHTTP(w, r.WithContext(ctx))

	// After routing, use low-cardinality route pattern.
	route := RoutePatternFromCtx(r.Context())
	tracing.SpanSetName(span, r.Method+" "+route)

	status := state.Status()
	// Emit http.status_code as int64 for cross-span type consistency with
	// OTel collector pipelines that switch on attribute type.
	span.SetAttributes(
		tracing.Attr{Key: "http.route", Value: route},
		tracing.Attr{Key: "http.status_code", Value: int64(status)},
	)

	// Late-bind contract attributes collected by wrapper.HTTPHandler on
	// contract-bound routes. Empty only for framework-owned non-contract
	// endpoints or direct standalone middleware usage.
	if len(carrier.Attrs) > 0 {
		span.SetAttributes(carrier.Attrs...)
	}

	// 5xx → error span; 4xx and below → unset (otelhttp convention).
	if status >= 500 {
		tracing.SpanSetStatus(span, true, http.StatusText(status))
	}
}

type spanErrorRecorder struct {
	span     tracing.Span
	redactor wrapper.ErrorRedactor
}

type spanErrorRecorderKey struct{}

func withSpanErrorRecorder(ctx context.Context, span tracing.Span, redactor wrapper.ErrorRedactor) context.Context {
	return context.WithValue(ctx, spanErrorRecorderKey{}, spanErrorRecorder{span: span, redactor: redactor})
}

func recordPanicOnActiveSpan(ctx context.Context, rec any) {
	r, ok := ctx.Value(spanErrorRecorderKey{}).(spanErrorRecorder)
	if !ok || r.span == nil || rec == nil {
		return
	}
	err := panicAsError(rec)
	if r.redactor != nil {
		err = r.redactor(err)
	}
	if err != nil {
		r.span.RecordError(err)
	}
}

func panicAsError(rec any) error {
	switch v := rec.(type) {
	case nil:
		return nil
	case error:
		return v
	case string:
		return errors.New(v)
	default:
		return fmt.Errorf("panic: %v", v)
	}
}
