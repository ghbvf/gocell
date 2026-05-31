package interceptor

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
)

func TestUnaryRecovery(t *testing.T) {
	info := &grpc.UnaryServerInfo{FullMethod: "/pkg.Svc/Do"}

	t.Run("non-panicking handler passes through", func(t *testing.T) {
		want := "ok"
		resp, err := UnaryRecovery()(context.Background(), nil, info,
			func(context.Context, any) (any, error) { return want, nil })
		if err != nil {
			t.Fatalf("err = %v, want nil", err)
		}
		if resp != want {
			t.Fatalf("resp = %v, want %v", resp, want)
		}
	})

	t.Run("panic converts to codes.Internal with redacted slog", func(t *testing.T) {
		var buf bytes.Buffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
		defer slog.SetDefault(prev)

		resp, err := UnaryRecovery()(context.Background(), nil, info,
			func(context.Context, any) (any, error) {
				panic("password=hunter2 boom")
			})

		if resp != nil {
			t.Fatalf("resp = %v, want nil after panic", resp)
		}
		if status.Code(err) != codes.Internal {
			t.Fatalf("code = %v, want Internal", status.Code(err))
		}
		out := buf.String()
		if !strings.Contains(out, "panic recovered") {
			t.Fatalf("slog missing panic log: %s", out)
		}
		if strings.Contains(out, "hunter2") {
			t.Fatalf("slog leaked secret (not redacted): %s", out)
		}
		if !strings.Contains(out, "<REDACTED>") {
			t.Fatalf("slog missing redaction marker: %s", out)
		}
	})

	t.Run("panic log carries request_id from ctx", func(t *testing.T) {
		var buf bytes.Buffer
		prev := slog.Default()
		slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
		defer slog.SetDefault(prev)

		ctx := ctxkeys.WithRequestID(context.Background(), "req-xyz")
		_, _ = UnaryRecovery()(ctx, nil, info,
			func(context.Context, any) (any, error) { panic("boom") })

		if !strings.Contains(buf.String(), `"request_id":"req-xyz"`) {
			t.Fatalf("panic log missing request_id: %s", buf.String())
		}
	})
}
