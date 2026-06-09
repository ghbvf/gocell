package interceptor

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

func TestUnaryTracing(t *testing.T) {
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Do"}

	t.Run("success sets rpc attributes and ends span", func(t *testing.T) {
		assertUnaryTracingSuccess(t, info)
	})

	t.Run("error records error and sets error status", func(t *testing.T) {
		assertUnaryTracingError(t, info)
	})

	t.Run("nil tracer degrades to noop", func(t *testing.T) {
		assertUnaryTracingNilTracer(t, info)
	})
}

func assertUnaryTracingSuccess(t *testing.T, info *grpc.UnaryServerInfo) {
	t.Helper()

	tr := &recordingTracer{span: &recordingSpan{}}
	_, err := UnaryTracing(tr)(context.Background(), nil, info,
		func(context.Context, any) (any, error) { return "ok", nil })
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if tr.span.name != "pkg.Svc/Do" {
		t.Fatalf("span name = %q, want pkg.Svc/Do", tr.span.name)
	}
	if !tr.span.ended {
		t.Fatalf("span not ended")
	}
	if v, ok := tr.span.attr("rpc.system"); !ok || v != "grpc" {
		t.Fatalf("rpc.system = %v ok=%v", v, ok)
	}
	if v, ok := tr.span.attr("rpc.service"); !ok || v != "pkg.Svc" {
		t.Fatalf("rpc.service = %v ok=%v", v, ok)
	}
	if v, ok := tr.span.attr("rpc.method"); !ok || v != "Do" {
		t.Fatalf("rpc.method = %v ok=%v", v, ok)
	}
	if v, ok := tr.span.attr("rpc.grpc.status_code"); !ok || v != int64(codes.OK) {
		t.Fatalf("status_code = %v ok=%v, want %d", v, ok, codes.OK)
	}
	if tr.span.recordedErr != nil {
		t.Fatalf("unexpected recorded error: %v", tr.span.recordedErr)
	}
}

func assertUnaryTracingError(t *testing.T, info *grpc.UnaryServerInfo) {
	t.Helper()

	tr := &recordingTracer{span: &recordingSpan{}}
	_, err := UnaryTracing(tr)(context.Background(), nil, info,
		func(context.Context, any) (any, error) { return nil, status.Error(codes.NotFound, "nope") })
	if status.Code(err) != codes.NotFound {
		t.Fatalf("code = %v", status.Code(err))
	}
	if tr.span.recordedErr == nil {
		t.Fatalf("expected RecordError to be called")
	}
	if tr.span.statusCode != wrapper.StatusError {
		t.Fatalf("span status = %v, want StatusError", tr.span.statusCode)
	}
	if v, _ := tr.span.attr("rpc.grpc.status_code"); v != int64(codes.NotFound) {
		t.Fatalf("status_code attr = %v, want %d", v, codes.NotFound)
	}
}

func assertUnaryTracingNilTracer(t *testing.T, info *grpc.UnaryServerInfo) {
	t.Helper()

	// Must not panic.
	_, err := UnaryTracing(nil)(context.Background(), nil, info,
		func(context.Context, any) (any, error) { return nil, errors.New("x") })
	if err == nil {
		t.Fatalf("expected handler error to propagate")
	}
}

func TestUnaryTracingPropagation(t *testing.T) {
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Do"}

	t.Run("W3C traceparent continues upstream trace", func(t *testing.T) {
		const traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
		md := metadata.Pairs("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
		ctx := metadata.NewIncomingContext(context.Background(), md)

		tr := &recordingTracer{span: &recordingSpan{}}
		var seen string
		_, _ = UnaryTracing(tr)(ctx, nil, info, func(c context.Context, _ any) (any, error) {
			seen, _ = ctxkeys.TraceIDFrom(c)
			return "ok", nil
		})
		if seen != traceID {
			t.Fatalf("trace id = %q, want %q (W3C parent not continued)", seen, traceID)
		}
	})

	t.Run("no metadata starts a fresh root", func(t *testing.T) {
		tr := &recordingTracer{span: &recordingSpan{}}
		var present bool
		_, _ = UnaryTracing(tr)(context.Background(), nil, info, func(c context.Context, _ any) (any, error) {
			_, present = ctxkeys.TraceIDFrom(c)
			return "ok", nil
		})
		if present {
			t.Fatalf("no upstream trace id should be mirrored when metadata is absent")
		}
	})

	// F6: b3 fallback and W3C precedence.

	t.Run("b3 single-header continues upstream trace", func(t *testing.T) {
		// b3 single-header format: {traceID}-{spanID}-{flags}
		const traceID = "a3ce929d0e0e47364bf92f3577b34da6"
		md := metadata.Pairs("b3", traceID+"-00f067aa0ba902b7-1")
		ctx := metadata.NewIncomingContext(context.Background(), md)

		tr := &recordingTracer{span: &recordingSpan{}}
		var seen string
		_, _ = UnaryTracing(tr)(ctx, nil, info, func(c context.Context, _ any) (any, error) {
			seen, _ = ctxkeys.TraceIDFrom(c)
			return "ok", nil
		})
		if seen != traceID {
			t.Fatalf("trace id = %q, want %q (b3 single-header not continued)", seen, traceID)
		}
	})

	t.Run("b3 multi-header continues upstream trace", func(t *testing.T) {
		// b3 multi-header: x-b3-traceid / x-b3-spanid / x-b3-sampled
		const traceID = "b3ce929d0e0e47364bf92f3577b34da6"
		md := metadata.Pairs(
			"x-b3-traceid", traceID,
			"x-b3-spanid", "00f067aa0ba902b7",
			"x-b3-sampled", "1",
		)
		ctx := metadata.NewIncomingContext(context.Background(), md)

		tr := &recordingTracer{span: &recordingSpan{}}
		var seen string
		_, _ = UnaryTracing(tr)(ctx, nil, info, func(c context.Context, _ any) (any, error) {
			seen, _ = ctxkeys.TraceIDFrom(c)
			return "ok", nil
		})
		if seen != traceID {
			t.Fatalf("trace id = %q, want %q (b3 multi-header not continued)", seen, traceID)
		}
	})

	t.Run("invalid W3C traceparent falls back to b3", func(t *testing.T) {
		// Malformed traceparent causes W3C to extract no remote span context;
		// b3 single-header must be used as fallback.
		const traceID = "c3ce929d0e0e47364bf92f3577b34da6"
		md := metadata.Pairs(
			"traceparent", "not-a-valid-traceparent",
			"b3", traceID+"-00f067aa0ba902b7-1",
		)
		ctx := metadata.NewIncomingContext(context.Background(), md)

		tr := &recordingTracer{span: &recordingSpan{}}
		var seen string
		_, _ = UnaryTracing(tr)(ctx, nil, info, func(c context.Context, _ any) (any, error) {
			seen, _ = ctxkeys.TraceIDFrom(c)
			return "ok", nil
		})
		if seen != traceID {
			t.Fatalf("trace id = %q, want %q (b3 fallback after invalid W3C not working)", seen, traceID)
		}
	})

	t.Run("W3C takes precedence over b3 when both present", func(t *testing.T) {
		// Both W3C traceparent and b3 are present; W3C must win.
		const w3cTraceID = "4bf92f3577b34da6a3ce929d0e0e4736"
		const b3TraceID = "d3ce929d0e0e47364bf92f3577b34da6"
		md := metadata.Pairs(
			"traceparent", "00-"+w3cTraceID+"-00f067aa0ba902b7-01",
			"b3", b3TraceID+"-00f067aa0ba902b7-1",
		)
		ctx := metadata.NewIncomingContext(context.Background(), md)

		tr := &recordingTracer{span: &recordingSpan{}}
		var seen string
		_, _ = UnaryTracing(tr)(ctx, nil, info, func(c context.Context, _ any) (any, error) {
			seen, _ = ctxkeys.TraceIDFrom(c)
			return "ok", nil
		})
		if seen != w3cTraceID {
			t.Fatalf("trace id = %q, want W3C id %q (W3C must take precedence over b3)", seen, w3cTraceID)
		}
	})
}

func TestMetadataCarrier(t *testing.T) {
	md := metadata.Pairs("traceparent", "abc", "x-b3-traceid", "def")
	c := metadataCarrier(md)
	if got := c.Get("traceparent"); got != "abc" {
		t.Fatalf("Get(traceparent) = %q, want abc", got)
	}
	if got := c.Get("absent"); got != "" {
		t.Fatalf("Get(absent) = %q, want empty", got)
	}
	keys := c.Keys()
	if len(keys) != 2 {
		t.Fatalf("Keys() = %v, want 2 keys", keys)
	}
	c.Set("newkey", "val")
	if got := c.Get("newkey"); got != "val" {
		t.Fatalf("Set/Get roundtrip failed: %q", got)
	}
}

func TestSplitFullMethod(t *testing.T) {
	tests := []struct{ in, svc, method string }{
		{"/pkg.Svc/Do", "pkg.Svc", "Do"},
		{"pkg.Svc/Do", "pkg.Svc", "Do"},
		{"/Do", "Do", ""},
	}
	for _, tt := range tests {
		svc, method := splitFullMethod(tt.in)
		if svc != tt.svc || method != tt.method {
			t.Fatalf("splitFullMethod(%q) = (%q,%q), want (%q,%q)", tt.in, svc, method, tt.svc, tt.method)
		}
	}
}
