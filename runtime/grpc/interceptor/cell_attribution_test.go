package interceptor

import (
	"context"
	"testing"

	"google.golang.org/grpc"

	kernelctxkeys "github.com/ghbvf/gocell/kernel/ctxkeys"
)

// TestUnaryCellAttribution verifies the interceptor writes the resolved cell id
// into ctx for a registered method, and writes nothing for an unregistered one
// (so metrics/access-log downstream degrade to the runtime sentinel).
func TestUnaryCellAttribution(t *testing.T) {
	const method = "/pkg.Svc/Do"
	info := &grpc.UnaryServerInfo{FullMethod: method}

	t.Run("writes resolved cell id into ctx for a known method", func(t *testing.T) {
		resolve := func(fullMethod string) (string, bool) {
			if fullMethod == method {
				return "mycell", true
			}
			return "", false
		}
		var got string
		var ok bool
		handler := func(ctx context.Context, _ any) (any, error) {
			got, ok = kernelctxkeys.CellIDFrom(ctx)
			return struct{}{}, nil
		}
		_, _ = UnaryCellAttribution(resolve)(context.Background(), nil, info, handler)
		if !ok || got != "mycell" {
			t.Fatalf("CellIDFrom = (%q,%v), want (mycell,true)", got, ok)
		}
	})

	t.Run("writes nothing for an unregistered method", func(t *testing.T) {
		resolve := func(string) (string, bool) { return "", false }
		var present bool
		handler := func(ctx context.Context, _ any) (any, error) {
			_, present = kernelctxkeys.CellIDFrom(ctx)
			return struct{}{}, nil
		}
		unknown := &grpc.UnaryServerInfo{FullMethod: "/grpc.health.v1.Health/Check"}
		_, _ = UnaryCellAttribution(resolve)(context.Background(), nil, unknown, handler)
		if present {
			t.Fatalf("CellIDFrom present for an unregistered method; want absent (→ sentinel downstream)")
		}
	})
}

func TestUnaryCellAttributionNilResolverPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatalf("UnaryCellAttribution with nil resolver must panic at construction")
		}
	}()
	_ = UnaryCellAttribution(nil)
}
