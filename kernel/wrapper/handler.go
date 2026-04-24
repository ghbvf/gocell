package wrapper

import (
	"fmt"
	"net/http"

	"github.com/ghbvf/gocell/kernel/ctxkeys"
)

// Option configures an HTTPHandler or WrapConsumer invocation. Options are
// applied in order; later options override earlier.
type Option func(*config)

type config struct {
	tracer     Tracer
	filter     func(*http.Request) bool
	spanNamer  func(ContractSpec, *http.Request) string
	eventNamer func(ContractSpec) string
	extraAttrs func(*http.Request) []Attr
}

func defaults() config {
	return config{
		tracer:     NoopTracer{},
		spanNamer:  defaultHTTPSpanName,
		eventNamer: defaultEventSpanName,
	}
}

// WithTracer injects a Tracer. Defaults to NoopTracer.
func WithTracer(t Tracer) Option {
	return func(c *config) {
		if t != nil {
			c.tracer = t
		}
	}
}

// WithFilter installs a predicate that, when true, bypasses tracing entirely —
// no span is started and the inner handler is invoked directly. Use for
// health probes or high-rate internal paths where span volume is cost-prohibitive.
//
// ref: open-telemetry/opentelemetry-go-contrib otelhttp/config.go — Filter type.
func WithFilter(pred func(*http.Request) bool) Option {
	return func(c *config) {
		if pred != nil {
			c.filter = pred
		}
	}
}

// WithSpanNameFormatter overrides the default HTTP span name (which is
// "{METHOD} {path_template}").
//
// ref: open-telemetry/opentelemetry-go-contrib otelhttp/config.go —
// WithSpanNameFormatter option.
func WithSpanNameFormatter(fn func(ContractSpec, *http.Request) string) Option {
	return func(c *config) {
		if fn != nil {
			c.spanNamer = fn
		}
	}
}

// WithConsumerSpanNameFormatter overrides the default event consumer span
// name (which is "CONSUME {topic}").
func WithConsumerSpanNameFormatter(fn func(ContractSpec) string) Option {
	return func(c *config) {
		if fn != nil {
			c.eventNamer = fn
		}
	}
}

// WithExtraAttrs supplies request-specific span attributes (e.g. resolved
// user id, tenant id). The closure runs once per request; return nil to
// skip attribute emission.
func WithExtraAttrs(fn func(*http.Request) []Attr) Option {
	return func(c *config) {
		if fn != nil {
			c.extraAttrs = fn
		}
	}
}

func defaultHTTPSpanName(spec ContractSpec, _ *http.Request) string {
	return spec.Method + " " + spec.Path
}

func defaultEventSpanName(spec ContractSpec) string {
	return "CONSUME " + spec.Topic
}

// HTTPHandler wraps next with a traced span + metric-friendly attributes
// derived from spec. The returned handler:
//   - starts a span named per the configured formatter
//   - sets gocell.contract.id / kind / transport, http.method / route attrs
//   - captures the response status code and sets StatusError for 5xx
//   - propagates contract id into the request context via ctxkeys.ContractID
//   - re-panics any handler panic, but records it + marks span status=Error first
//
// spec is validated at call time; invalid specs or nil handlers panic (fail-fast
// at registration time beats a silent miss at request time).
//
// ref: riandyrn/otelchi middleware.go — span name + status lifecycle.
func HTTPHandler(spec ContractSpec, next http.Handler, opts ...Option) http.Handler {
	validateHTTPHandlerArgs(spec, next)

	cfg := defaults()
	for _, opt := range opts {
		opt(&cfg)
	}
	baseAttrs := httpBaseAttrs(spec)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if cfg.filter != nil && cfg.filter(r) {
			next.ServeHTTP(w, r)
			return
		}
		serveTraced(cfg, baseAttrs, spec, next, w, r)
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

func serveTraced(cfg config, baseAttrs []Attr, spec ContractSpec, next http.Handler, w http.ResponseWriter, r *http.Request) {
	ctx := ctxkeys.WithContractID(r.Context(), spec.ID)
	ctx, span := cfg.tracer.Start(ctx, cfg.spanNamer(spec, r))
	span.SetAttributes(baseAttrs...)
	if cfg.extraAttrs != nil {
		if extra := cfg.extraAttrs(r); len(extra) > 0 {
			span.SetAttributes(extra...)
		}
	}

	sw := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
	defer func() {
		// recover must be called directly inside the deferred function body.
		finishSpan(span, sw, recover())
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
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(code int) {
	if !r.wroteHeader {
		r.status = code
		r.wroteHeader = true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *statusRecorder) Write(b []byte) (int, error) {
	if !r.wroteHeader {
		r.status = http.StatusOK
		r.wroteHeader = true
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap lets net/http discover the underlying ResponseWriter so stdlib
// features (Flusher, Hijacker, ...) continue to work when supported.
func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}
