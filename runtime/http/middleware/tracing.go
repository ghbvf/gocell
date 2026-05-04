package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/httputil"
	"github.com/ghbvf/gocell/pkg/redaction"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
)

// TracingOption configures the Tracing middleware.
type TracingOption func(*tracingConfig)

// ContractAttrsResolver resolves contract-derived span attributes from a
// concrete request method/path. Routers use this to annotate spans when
// pre-handler middleware short-circuits before wrapper.HTTPHandler can append
// to the AttrCarrier.
type ContractAttrsResolver func(method, path string) ([]wrapper.Attr, bool)

type tracingConfig struct {
	publicEndpointFn func(*http.Request) bool
	skipFn           func(*http.Request) bool
	contractAttrs    ContractAttrsResolver
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

// WithContractAttrsResolver installs a resolver that can provide contract
// attrs without waiting for the leaf wrapper.HTTPHandler. This keeps
// rate-limit/auth/body-limit short-circuits tagged with the same
// gocell.contract.* metadata as successful handler executions.
func WithContractAttrsResolver(fn ContractAttrsResolver) TracingOption {
	return func(c *tracingConfig) {
		if fn != nil {
			c.contractAttrs = fn
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
// finalizes the span. Extracted to keep Tracing's cognitive complexity ≤ 15.
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

	// Install a cancel-reason slot so writeErrcodeError can record the
	// 499 originating ctx error ("canceled" vs "deadline_exceeded") for
	// the post-handler span attribute below. Without this slot installed,
	// 499 spans fall back to the legacy "context_canceled" label.
	ctx = httputil.WithCancelReasonSlot(ctx)

	// Start span with tentative name using raw path.
	// After routing, the span is renamed to use the route pattern.
	ctx, span := tracer.Start(ctx, r.Method+" "+r.URL.Path)
	ctx = withSpanErrorRecorder(ctx, span)
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

	status := state.Status()
	route, contractAttrs := finalRouteAndContractAttrs(r, carrier, cfg)
	tracing.SpanSetName(span, r.Method+" "+route)

	// Emit http.status_code as int64 for cross-span type consistency with
	// OTel collector pipelines that switch on attribute type.
	span.SetAttributes(
		tracing.Attr{Key: "http.route", Value: route},
		tracing.Attr{Key: "http.status_code", Value: int64(status)},
	)

	// Late-bind contract attributes collected by wrapper.HTTPHandler on
	// contract-bound routes. If the request short-circuited before the leaf
	// handler, fall back to the router-supplied contract resolver.
	if len(contractAttrs) > 0 {
		span.SetAttributes(contractAttrs...)
	}

	// 499 (client closed request): preserve OTel HTTP semantic conventions —
	// SERVER span status MUST remain Unset for 4xx; intentional cancellation
	// SHOULD NOT set error.type. Record a structured attribute so operators
	// can distinguish client cancellation from other 4xx (validation / auth)
	// without polluting span error rate metrics. The 5xx branch below
	// naturally skips 499 because 499 < 500.
	//
	// Reason granularity: the reason value is sourced from the cancel-reason
	// slot populated by httputil.writeErrcodeError when ctxcancel.Wrap fed
	// the 499 path. Three values are possible on the span attribute:
	//
	//   "canceled"          — IO boundary returned ctxcancel.Wrap on
	//                          context.Canceled (real client disconnect).
	//   "deadline_exceeded" — IO boundary returned ctxcancel.Wrap on
	//                          context.DeadlineExceeded (server-side / inherited
	//                          timeout — investigate as a server timeout, not
	//                          a noisy client).
	//   "context_canceled"  — fallback: a handler emitted a raw 499 status
	//                          without going through ctxcancel.Wrap, so the
	//                          originating ctx error is not knowable here.
	//                          Distinct from "canceled" on purpose: dashboards
	//                          surface this bucket as "instrumentation gap"
	//                          (operator should route the IO site through
	//                          ctxcancel.Wrap to upgrade the signal).
	//
	// ref: open-telemetry/semantic-conventions http-spans.md — 4xx server
	//      span status Unset; intentional cancellation must not set error.type.
	if status == httputil.StatusClientClosedRequest {
		reason := httputil.CancelReason(ctx)
		if reason == "" {
			reason = "context_canceled"
		}
		span.SetAttributes(
			tracing.Attr{Key: "client.cancel.reason", Value: reason},
		)
	}

	// 5xx → error span; 4xx and below → unset (otelhttp convention).
	if status >= 500 {
		tracing.SpanSetStatus(span, true, http.StatusText(status))
	}
}

func finalRouteAndContractAttrs(
	r *http.Request,
	carrier *wrapper.AttrCarrier,
	cfg tracingConfig,
) (string, []wrapper.Attr) {
	route := RouteFor(r.Context(), r.Method, r.URL.Path)
	if len(carrier.Attrs) > 0 {
		return route, carrier.Attrs
	}
	if cfg.contractAttrs == nil {
		return route, nil
	}
	attrs, ok := cfg.contractAttrs(r.Method, r.URL.Path)
	if !ok {
		return route, nil
	}
	if contractRoute := attrString(attrs, "http.route"); contractRoute != "" {
		route = contractRoute
	}
	return route, attrs
}

func attrString(attrs []wrapper.Attr, key string) string {
	for _, a := range attrs {
		if a.Key == key {
			if v, ok := a.Value.(string); ok {
				return v
			}
		}
	}
	return ""
}

type spanErrorRecorder struct {
	span tracing.Span
}

type spanErrorRecorderKey struct{}

func withSpanErrorRecorder(ctx context.Context, span tracing.Span) context.Context {
	return context.WithValue(ctx, spanErrorRecorderKey{}, spanErrorRecorder{span: span})
}

// recordPanicOnActiveSpan attaches a panic to the active HTTP span. The
// panic message is hardcoded through pkg/redaction.RedactError before
// span.RecordError; there is no caller-side opt-out (see pkg/redaction).
//
// The redaction call is inlined as the RecordError argument so the
// SPAN-RECORD-ERROR-REDACT-01 archtest gate can statically verify the wrap.
func recordPanicOnActiveSpan(ctx context.Context, rec any) {
	r, ok := ctx.Value(spanErrorRecorderKey{}).(spanErrorRecorder)
	if !ok || r.span == nil || rec == nil {
		return
	}
	if err := panicAsError(rec); err != nil {
		r.span.RecordError(redaction.RedactError(err))
	}
	tracing.SpanSetStatus(r.span, true, "panic")
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
