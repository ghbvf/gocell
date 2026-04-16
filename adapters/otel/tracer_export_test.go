package otel_test

import (
	"context"
	"testing"

	gcotel "github.com/ghbvf/gocell/adapters/otel"
	"github.com/ghbvf/gocell/runtime/observability/tracing"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// newInMemoryTracer returns a Tracer wired to a TracerProvider that batches
// into an InMemoryExporter. Returned emitter() forces a flush and snapshots
// all spans collected so far.
//
// ref: opentelemetry-go sdk/trace/tracetest@main — InMemoryExporter is the
// recommended test fixture for span-shape assertions. We use a
// SimpleSpanProcessor (synchronous) rather than Batcher so flushes are
// immediate and tests do not have to sleep.
func newInMemoryTracer(t *testing.T) (*gcotel.Tracer, *tracetest.InMemoryExporter, *sdktrace.TracerProvider) {
	t.Helper()
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSpanProcessor(sdktrace.NewSimpleSpanProcessor(exp)),
	)
	tracer, err := gcotel.NewTracerFromTracerProvider(tp, "gocell.test")
	if err != nil {
		t.Fatalf("NewTracerFromTracerProvider: %v", err)
	}
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
	})
	return tracer, exp, tp
}

func TestTracer_SpanNameAndAttributes(t *testing.T) {
	tr, exp, _ := newInMemoryTracer(t)

	ctx, span := tr.Start(context.Background(), "outer-op")
	span.SetAttribute("cell.id", "access-core")
	span.SetAttribute("retry.count", 3)
	tracing.SpanSetStatus(span, false, "")
	span.End()

	_ = ctx
	got := exp.GetSpans()
	if len(got) != 1 {
		t.Fatalf("want 1 span, got %d", len(got))
	}
	s := got[0]
	if s.Name != "outer-op" {
		t.Fatalf("span name = %q, want outer-op", s.Name)
	}
	gotCellID := false
	gotRetry := false
	for _, kv := range s.Attributes {
		if string(kv.Key) == "cell.id" && kv.Value.AsString() == "access-core" {
			gotCellID = true
		}
		if string(kv.Key) == "retry.count" && kv.Value.AsInt64() == 3 {
			gotRetry = true
		}
	}
	if !gotCellID {
		t.Errorf("span missing cell.id attribute, got %#v", s.Attributes)
	}
	if !gotRetry {
		t.Errorf("span missing retry.count attribute, got %#v", s.Attributes)
	}
}

func TestTracer_ParentChildRelationship(t *testing.T) {
	tr, exp, _ := newInMemoryTracer(t)

	ctx, parent := tr.Start(context.Background(), "parent")
	_, child := tr.Start(ctx, "child")
	child.End()
	parent.End()

	spans := exp.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("want 2 spans, got %d", len(spans))
	}
	// Spans come out in end-order (child first).
	childSpan := spans[0]
	parentSpan := spans[1]
	if childSpan.Name != "child" || parentSpan.Name != "parent" {
		t.Fatalf("unexpected order: %q, %q", childSpan.Name, parentSpan.Name)
	}
	if childSpan.Parent.SpanID() != parentSpan.SpanContext.SpanID() {
		t.Fatalf("child Parent.SpanID mismatch: got %v, want %v",
			childSpan.Parent.SpanID(), parentSpan.SpanContext.SpanID())
	}
	if childSpan.SpanContext.TraceID() != parentSpan.SpanContext.TraceID() {
		t.Fatal("parent and child trace IDs differ")
	}
}

func TestTracer_ErrorStatusRecordsSpan(t *testing.T) {
	tr, exp, _ := newInMemoryTracer(t)

	_, span := tr.Start(context.Background(), "err-op")
	tracing.SpanRecordError(span, errTracerBoom)
	tracing.SpanSetStatus(span, true, "boom")
	span.End()

	spans := exp.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("want 1 span, got %d", len(spans))
	}
	if spans[0].Status.Description != "boom" {
		t.Fatalf("status description = %q, want boom", spans[0].Status.Description)
	}
	if len(spans[0].Events) == 0 {
		t.Fatal("expected RecordError to emit an event")
	}
}

func TestNewTracerFromTracerProvider_RejectsNil(t *testing.T) {
	if _, err := gcotel.NewTracerFromTracerProvider(nil, "svc"); err == nil {
		t.Fatal("nil TracerProvider must be rejected")
	}
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	if _, err := gcotel.NewTracerFromTracerProvider(tp, ""); err == nil {
		t.Fatal("empty serviceName must be rejected")
	}
}

var errTracerBoom = &tracerTestError{msg: "boom"}

type tracerTestError struct{ msg string }

func (e *tracerTestError) Error() string { return e.msg }
