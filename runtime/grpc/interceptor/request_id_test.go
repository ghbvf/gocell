package interceptor

import (
	"context"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

func TestUnaryRequestID(t *testing.T) {
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Do"}

	capture := func(ctx context.Context) (reqID, corrID string) {
		r, _ := ctxkeys.RequestIDFrom(ctx)
		c, _ := ctxkeys.CorrelationIDFrom(ctx)
		return r, c
	}

	t.Run("valid incoming id is propagated", func(t *testing.T) {
		md := metadata.Pairs(requestIDMetadataKey, "abc-123")
		ctx := metadata.NewIncomingContext(context.Background(), md)
		var reqID, corrID string
		_, err := UnaryRequestID()(ctx, nil, info, func(c context.Context, _ any) (any, error) {
			reqID, corrID = capture(c)
			return "ok", nil
		})
		if err != nil {
			t.Fatalf("err = %v", err)
		}
		if reqID != "abc-123" || corrID != "abc-123" {
			t.Fatalf("req=%q corr=%q, want abc-123", reqID, corrID)
		}
	})

	t.Run("invalid incoming id is replaced by generated", func(t *testing.T) {
		md := metadata.Pairs(requestIDMetadataKey, "bad id with spaces!")
		ctx := metadata.NewIncomingContext(context.Background(), md)
		var reqID string
		_, _ = UnaryRequestID()(ctx, nil, info, func(c context.Context, _ any) (any, error) {
			reqID, _ = capture(c)
			return "ok", nil
		})
		if reqID == "" || reqID == "bad id with spaces!" {
			t.Fatalf("reqID = %q, want a fresh generated id", reqID)
		}
	})

	t.Run("missing id is generated and correlation matches", func(t *testing.T) {
		var reqID, corrID string
		_, _ = UnaryRequestID()(context.Background(), nil, info, func(c context.Context, _ any) (any, error) {
			reqID, corrID = capture(c)
			return "ok", nil
		})
		if reqID == "" {
			t.Fatalf("reqID empty, want generated")
		}
		if reqID != corrID {
			t.Fatalf("req=%q != corr=%q", reqID, corrID)
		}
	})
}
