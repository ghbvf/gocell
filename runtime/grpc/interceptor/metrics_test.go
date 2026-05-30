package interceptor

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/observability/metrics"
)

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
			_, _ = UnaryMetrics(coll, clock.Real())(context.Background(), nil, info, tt.handler)
			// cell defaults to the runtime sentinel until gRPC cell attribution
			// is wired.
			if got := coll.Count("_runtime", method, tt.wantCode); got != 1 {
				t.Fatalf("count[%s] = %d, want 1", tt.wantCode, got)
			}
		})
	}
}
