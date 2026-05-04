package otel

import (
	"context"

	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	oteltrace "go.opentelemetry.io/otel/trace"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
)

// Compile-time check: Tracer implements tracing.Tracer.
var _ tracing.Tracer = (*Tracer)(nil)

// Tracer implements tracing.Tracer using the OpenTelemetry SDK.
type Tracer struct {
	inner oteltrace.Tracer
}

// NewTracer creates an OTel-backed Tracer with an OTLP gRPC exporter.
// It returns the tracer, a shutdown function, and any initialization error.
// The shutdown function flushes pending spans and releases resources.
func NewTracer(ctx context.Context, cfg TracerConfig) (*Tracer, func(context.Context) error, error) {
	if err := cfg.validate(); err != nil {
		return nil, nil, errcode.Wrap(errcode.KindInternal, ErrAdapterOTelConfig, err.Error(), err)
	}
	cfg.defaults()

	if cfg.ServiceName == "" {
		return nil, nil, errcode.New(errcode.KindInternal, ErrAdapterOTelConfig, "otel: ServiceName is required")
	}
	if cfg.ExporterEndpoint == "" {
		return nil, nil, errcode.New(errcode.KindInternal, ErrAdapterOTelConfig, "otel: ExporterEndpoint is required")
	}

	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(cfg.ExporterEndpoint),
	}
	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}

	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, nil, errcode.Wrap(errcode.KindInternal, ErrAdapterOTelInit, "otel: create OTLP exporter", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(cfg.ServiceName)),
	)
	if err != nil {
		return nil, nil, errcode.Wrap(errcode.KindInternal, ErrAdapterOTelInit, "otel: create resource", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(cfg.SampleRate)),
	)

	shutdown := func(shutdownCtx context.Context) error {
		if shutdownErr := tp.Shutdown(shutdownCtx); shutdownErr != nil {
			return errcode.Wrap(errcode.KindInternal, ErrAdapterOTelShutdown, "otel: shutdown tracer provider", shutdownErr)
		}
		return nil
	}

	return &Tracer{inner: tp.Tracer(cfg.ServiceName)}, shutdown, nil
}

// NewTracerFromTracerProvider wraps a caller-owned TracerProvider into a
// Tracer. The caller retains lifecycle ownership (no shutdown is returned).
//
// This constructor exists so advanced callers can compose their own
// exporter stack (e.g. a fan-out to OTLP + a local in-memory exporter),
// and so tests can substitute `sdktrace/tracetest.InMemoryExporter` for
// deterministic assertions on emitted spans without reaching through the
// OTLP gRPC path.
//
// ref: opentelemetry-go sdk/trace/tracetest/recorder.go@main — the SDK's
// own tests use tracetest.InMemoryExporter composed into a TracerProvider
// for the same reason.
func NewTracerFromTracerProvider(tp oteltrace.TracerProvider, serviceName string) (*Tracer, error) {
	if tp == nil {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterOTelConfig, "otel: TracerProvider is required")
	}
	if serviceName == "" {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterOTelConfig, "otel: serviceName is required")
	}
	return &Tracer{inner: tp.Tracer(serviceName)}, nil
}

// Start creates a new span with the given name. The returned context carries
// the span and its trace/span IDs propagated via ctxkeys. Accepts variadic
// wrapper.Attr so kernel/wrapper callers can hand attributes in at Start;
// attrs are applied on the returned Span immediately via SetAttributes.
func (t *Tracer) Start(ctx context.Context, name string, attrs ...tracing.Attr) (context.Context, tracing.Span) {
	if traceparent, ok := ctxkeys.TraceParentFrom(ctx); ok && traceparent != "" {
		ctx = propagation.TraceContext{}.Extract(ctx, propagation.MapCarrier{"traceparent": traceparent})
	}
	ctx, span := t.inner.Start(ctx, name)
	sc := span.SpanContext()
	ctx = ctxkeys.WithTraceID(ctx, sc.TraceID().String())
	ctx = ctxkeys.WithSpanID(ctx, sc.SpanID().String())
	ctx = ctxkeys.WithTraceParent(ctx,
		"00-"+sc.TraceID().String()+"-"+sc.SpanID().String()+"-"+sc.TraceFlags().String())
	out := &otelSpan{inner: span}
	if len(attrs) > 0 {
		out.SetAttributes(attrs...)
	}
	return ctx, out
}
