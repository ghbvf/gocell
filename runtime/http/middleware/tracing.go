package middleware

import (
	"net/http"

	"github.com/ghbvf/gocell/runtime/observability/tracing"
	oteltrace "go.opentelemetry.io/otel/trace"
)

// TracingOption configures the Tracing middleware.
type TracingOption func(*tracingConfig)

type tracingConfig struct {
	publicEndpointFn func(*http.Request) bool
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
// middleware), Tracing reuses it. Otherwise it creates its own to
// capture http.status_code as a standalone middleware.
func Tracing(tracer tracing.Tracer, opts ...TracingOption) func(http.Handler) http.Handler {
	var cfg tracingConfig
	for _, o := range opts {
		o(&cfg)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

			// Start span with tentative name using raw path.
			// After routing, the span is renamed to use the route pattern.
			ctx, span := tracer.Start(ctx, r.Method+" "+r.URL.Path)
			defer span.End()

			// Record linked remote context for public endpoints.
			if isPublic && remoteSpanCtx.IsValid() && remoteSpanCtx.IsRemote() {
				span.SetAttribute("linked.trace_id", remoteSpanCtx.TraceID().String())
				span.SetAttribute("linked.span_id", remoteSpanCtx.SpanID().String())
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
			span.SetAttribute("http.route", route)

			status := state.Status()
			span.SetAttribute("http.status_code", status)

			// 5xx → error span; 4xx and below → unset (otelhttp convention).
			if status >= 500 {
				tracing.SpanSetStatus(span, true, http.StatusText(status))
			}
		})
	}
}
