package interceptor

import (
	"context"
	"errors"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/kernel/wrapper"
)

func TestUnaryTracing(t *testing.T) {
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Do"}

	t.Run("success sets rpc attributes and ends span", func(t *testing.T) {
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
	})

	t.Run("error records error and sets error status", func(t *testing.T) {
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
	})

	t.Run("nil tracer degrades to noop", func(t *testing.T) {
		// Must not panic.
		_, err := UnaryTracing(nil)(context.Background(), nil, info,
			func(context.Context, any) (any, error) { return nil, errors.New("x") })
		if err == nil {
			t.Fatalf("expected handler error to propagate")
		}
	})
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
