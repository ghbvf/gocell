package wrapper

import (
	"fmt"
	"net/http"
	"sync"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
)

// HandlerOption configures an HTTPHandler invocation. Only filter-related
// options remain; Tracer ownership lives on the caller side (runtime/http
// /router — passed explicitly to HTTPHandler).
type HandlerOption func(*handlerConfig)

type handlerConfig struct {
	filter func(*http.Request) bool
}

// WithFilter installs a predicate that, when true, bypasses tracing entirely —
// no span is started and the inner handler is invoked directly. Use for
// health probes or high-rate internal paths where span volume is cost-prohibitive.
//
// Example (health probes):
//
//	wrapper.HTTPHandler(tr, spec, h, wrapper.WithFilter(wrapper.DefaultProbeFilter))
//
// ref: open-telemetry/opentelemetry-go-contrib otelhttp/config.go — Filter type.
func WithFilter(pred func(*http.Request) bool) HandlerOption {
	return func(c *handlerConfig) {
		if pred != nil {
			c.filter = pred
		}
	}
}

// DefaultProbeFilter skips health/readiness/liveness probe paths. Compose via
// WithFilter to avoid emitting spans for high-frequency infra traffic.
//
// Matched paths (exact): /healthz, /readyz, /livez.
func DefaultProbeFilter(r *http.Request) bool {
	p := r.URL.Path
	return p == "/healthz" || p == "/readyz" || p == "/livez"
}

func defaultHTTPSpanName(spec ContractSpec, _ *http.Request) string {
	return spec.Method + " " + spec.Path
}

func defaultEventSpanName(spec ContractSpec) string {
	return "CONSUME " + spec.Topic
}

// HTTPHandler wraps next with a traced span + metric-friendly attributes
// derived from spec. The returned handler:
//   - starts a span named "{METHOD} {path_template}" using the supplied tracer
//   - sets gocell.contract.id / kind / transport, http.method / route attrs
//   - captures the response status code and sets StatusError for 5xx
//   - propagates contract id into the request context via ctxkeys.ContractID
//   - re-panics any handler panic, but records it + marks span status=Error first
//
// tr is the Tracer supplied by the runtime infrastructure (typically
// runtime/http/router.Router). A nil tr falls back to NoopTracer{} — spans
// are silently discarded rather than panicking — so callers that construct
// HTTPHandler without a wired tracer (early-boot, tests using stdlib muxes)
// still produce correctly-dispatched HTTP responses.
//
// spec is validated at call time; invalid specs or nil handlers panic
// (fail-fast at registration time beats a silent miss at request time).
//
// ref: riandyrn/otelchi middleware.go — span name + status lifecycle.
func HTTPHandler(tr Tracer, spec ContractSpec, next http.Handler, opts ...HandlerOption) http.Handler {
	validateHTTPHandlerArgs(spec, next)
	if tr == nil {
		tr = NoopTracer{}
	}

	var cfg handlerConfig
	for _, opt := range opts {
		opt(&cfg)
	}
	baseAttrs := httpBaseAttrs(spec)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.filter != nil && cfg.filter(r) {
			next.ServeHTTP(w, r)
			return
		}
		serveTraced(tr, baseAttrs, spec, next, w, r)
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

func serveTraced(tr Tracer, baseAttrs []Attr, spec ContractSpec, next http.Handler, w http.ResponseWriter, r *http.Request) {
	ctx := ctxkeys.WithContractID(r.Context(), spec.ID)
	ctx, span := tr.Start(ctx, defaultHTTPSpanName(spec, r))
	span.SetAttributes(baseAttrs...)

	sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	defer func() {
		// recover must be called directly inside the deferred function body.
		rec := recover()
		finishSpan(span, sw, rec)
	}()

	next.ServeHTTP(sw, r.WithContext(ctx))
}

// finishSpan records terminal status + error info on span and re-panics
// if the handler panicked — leaving response-writing recovery to the outer
// Recovery middleware.
func finishSpan(span Span, sw *statusRecorder, rec any) {
	span.SetAttributes(Attr{Key: "http.status_code", Value: int64(sw.status)})
	if rec != nil {
		span.SetStatus(StatusError, "handler panic")
		if err, ok := rec.(error); ok {
			span.RecordError(err)
		} else {
			span.RecordError(fmt.Errorf("panic: %v", rec))
		}
		span.End()
		panic(rec)
	}
	if sw.status >= 500 {
		span.SetStatus(StatusError, http.StatusText(sw.status))
	}
	span.End()
}

// statusRecorder captures the HTTP status code for span annotation.
// ref: open-telemetry/opentelemetry-go-contrib otelhttp — ResponseWriter wrap.
// We intentionally stop at status-code capture; body-size / hijack support
// can be added behind opt-in flags if needed.
//
// WriteHeader and Write are guarded by sync.Once so concurrent calls
// (e.g. handler + panic recovery path) cannot race on wroteHeader/status.
type statusRecorder struct {
	http.ResponseWriter
	once        sync.Once
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	r.once.Do(func() {
		r.status = code
		r.wroteHeader = true
	})
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	// statusRecorder.status is seeded to http.StatusOK at construction
	// (see serveTraced), so first Write without prior WriteHeader only
	// needs to mark the header as committed — the implicit 200 is
	// already in place. We track this flag so a subsequent WriteHeader
	// call cannot retroactively change the captured span attribute.
	r.once.Do(func() {
		r.wroteHeader = true
	})
	return r.ResponseWriter.Write(b)
}

// Unwrap lets net/http discover the underlying ResponseWriter so stdlib
// features (Flusher, Hijacker, ...) continue to work when supported.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
