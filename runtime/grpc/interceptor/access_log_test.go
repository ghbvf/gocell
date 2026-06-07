package interceptor

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/kernel/clock"
	kernelctxkeys "github.com/ghbvf/gocell/kernel/ctxkeys"
	pkgctxkeys "github.com/ghbvf/gocell/pkg/ctxkeys"
)

// TestUnaryAccessLog asserts the per-RPC structured slog line carries the
// HTTP-parity field set: method, code, duration_ms, cell_id, request_id,
// correlation_id, trace_id. Auth failures are logged because AccessLog is outer
// to Auth in the chain (the handler error propagates back through it).
func TestUnaryAccessLog(t *testing.T) {
	const method = "/pkg.Svc/Do"
	info := &grpc.UnaryServerInfo{FullMethod: method}

	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx := context.Background()
	ctx = kernelctxkeys.WithCellID(ctx, "mycell")
	ctx = pkgctxkeys.WithRequestID(ctx, "req-1")
	ctx = pkgctxkeys.WithCorrelationID(ctx, "corr-1")
	ctx = pkgctxkeys.WithTraceID(ctx, "trace-1")

	_, _ = UnaryAccessLog(clock.Real())(ctx, nil, info,
		func(context.Context, any) (any, error) { return nil, status.Error(codes.NotFound, "x") })

	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("UnaryAccessLog emitted no slog line")
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("slog line is not JSON: %v (%q)", err, line)
	}
	want := map[string]string{
		"method":         method,
		"code":           codes.NotFound.String(),
		"cell_id":        "mycell",
		"request_id":     "req-1",
		"correlation_id": "corr-1",
		"trace_id":       "trace-1",
	}
	for k, v := range want {
		if got, _ := rec[k].(string); got != v {
			t.Errorf("field %q = %q, want %q", k, got, v)
		}
	}
	if _, ok := rec["duration_ms"]; !ok {
		t.Errorf("missing duration_ms field")
	}
}

func TestUnaryAccessLogNilClockPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("UnaryAccessLog with nil clock must panic at construction")
		}
	}()
	_ = UnaryAccessLog(nil)
}
