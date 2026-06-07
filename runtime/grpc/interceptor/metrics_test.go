package interceptor

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/kernel/clock"
	kernelctxkeys "github.com/ghbvf/gocell/kernel/ctxkeys"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

// testCellSet builds the assembly closed set passed to UnaryMetrics in tests.
func testCellSet(ids ...string) map[string]struct{} {
	s := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		s[id] = struct{}{}
	}
	return s
}

func TestUnaryMetrics(t *testing.T) {
	const method = "/pkg.Svc/Do"
	info := &grpc.UnaryServerInfo{FullMethod: method}

	tests := []struct {
		name     string
		handler  grpc.UnaryHandler
		wantCode string
	}{
		{
			name:     "success records OK",
			handler:  func(context.Context, any) (any, error) { return "ok", nil },
			wantCode: codes.OK.String(),
		},
		{
			name:     "status error records its code",
			handler:  func(context.Context, any) (any, error) { return nil, status.Error(codes.NotFound, "x") },
			wantCode: codes.NotFound.String(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			coll := metrics.NewInMemoryGRPCCollector()
			// No cell in ctx → sentinel regardless of the closed set; the code
			// label is what this case exercises.
			_, _ = UnaryMetrics(coll, clock.Real(), testCellSet("mycell"))(context.Background(), nil, info, tt.handler)
			if got := coll.Count(metrics.RuntimeCellSentinel, method, tt.wantCode); got != 1 {
				t.Fatalf("count[%s] = %d, want 1", tt.wantCode, got)
			}
		})
	}

	t.Run("attributed cell in the closed set is reflected in the label", func(t *testing.T) {
		coll := metrics.NewInMemoryGRPCCollector()
		ctx := kernelctxkeys.WithCellID(context.Background(), "mycell")
		_, _ = UnaryMetrics(coll, clock.Real(), testCellSet("mycell"))(ctx, nil, info,
			func(context.Context, any) (any, error) { return "ok", nil })
		if got := coll.Count("mycell", method, codes.OK.String()); got != 1 {
			t.Fatalf("count[mycell] = %d, want 1 (attributed cell in closed set)", got)
		}
	})

	t.Run("attributed cell outside the closed set degrades to sentinel", func(t *testing.T) {
		coll := metrics.NewInMemoryGRPCCollector()
		ctx := kernelctxkeys.WithCellID(context.Background(), "intruder")
		_, _ = UnaryMetrics(coll, clock.Real(), testCellSet("mycell"))(ctx, nil, info,
			func(context.Context, any) (any, error) { return "ok", nil })
		if got := coll.Count(metrics.RuntimeCellSentinel, method, codes.OK.String()); got != 1 {
			t.Fatalf("count[_runtime] = %d, want 1 (out-of-set cell degrades to sentinel)", got)
		}
		if got := coll.Count("intruder", method, codes.OK.String()); got != 0 {
			t.Fatalf("count[intruder] = %d, want 0 (out-of-set cell must not pollute SLO series)", got)
		}
	})

	t.Run("no attributed cell degrades to sentinel", func(t *testing.T) {
		coll := metrics.NewInMemoryGRPCCollector()
		_, _ = UnaryMetrics(coll, clock.Real(), testCellSet("mycell"))(context.Background(), nil, info,
			func(context.Context, any) (any, error) { return "ok", nil })
		if got := coll.Count(metrics.RuntimeCellSentinel, method, codes.OK.String()); got != 1 {
			t.Fatalf("count[_runtime] = %d, want 1 (no cell attributed)", got)
		}
	})
}

func TestUnaryMetricsNilCollectorPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("UnaryMetrics with nil collector must panic at construction")
		}
	}()
	_ = UnaryMetrics(nil, clock.Real(), testCellSet("mycell"))
}
