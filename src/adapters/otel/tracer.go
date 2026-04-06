package otel

import (
	"context"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	oteltrace "go.opentelemetry.io/otel/trace"
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
	cfg.defaults()

	if cfg.ServiceName == "" {
		return nil, nil, errcode.New(ErrAdapterOTelConfig, "otel: ServiceName is required")
	}
	if cfg.ExporterEndpoint == "" {
		return nil, nil, errcode.New(ErrAdapterOTelConfig, "otel: ExporterEndpoint is required")
	}

	opts := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(cfg.ExporterEndpoint),
	}
	if cfg.Insecure {
		opts = append(opts, otlptracegrpc.WithInsecure())
	}

	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, nil, errcode.Wrap(ErrAdapterOTelInit, "otel: create OTLP exporter", err)
	}

	res, err := resource.New(ctx,
		resource.WithAttributes(semconv.ServiceName(cfg.ServiceName)),
	)
	if err != nil {
		return nil, nil, errcode.Wrap(ErrAdapterOTelInit, "otel: create resource", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.TraceIDRatioBased(cfg.SampleRate)),
	)

	shutdown := func(shutdownCtx context.Context) error {
		if shutdownErr := tp.Shutdown(shutdownCtx); shutdownErr != nil {
			return errcode.Wrap(ErrAdapterOTelShutdown, "otel: shutdown tracer provider", shutdownErr)
		}
		return nil
	}

	return &Tracer{inner: tp.Tracer(cfg.ServiceName)}, shutdown, nil
}

// Start creates a new span with the given name. The returned context carries
// the span and its trace/span IDs propagated via ctxkeys.
func (t *Tracer) Start(ctx context.Context, name string) (context.Context, tracing.Span) {
	ctx, span := t.inner.Start(ctx, name)
	sc := span.SpanContext()
	ctx = ctxkeys.WithTraceID(ctx, sc.TraceID().String())
	ctx = ctxkeys.WithSpanID(ctx, sc.SpanID().String())
	return ctx, &otelSpan{inner: span}
}
