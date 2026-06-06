package devicecommandrpc

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	commandv1 "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

var fixedTime = time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

func newTestServer() *Server {
	return NewServer(clockmock.New(fixedTime))
}

// TestServer_IssueCommand_Direct exercises the handler in-process (no transport):
// the success path returns a non-empty ack stamped from the injected clock, and
// each missing-field path returns an *errcode.Error (KindInvalid / ErrValidationFailed).
func TestServer_IssueCommand_Direct(t *testing.T) {
	t.Parallel()
	srv := newTestServer()

	t.Run("success", func(t *testing.T) {
		resp, err := srv.IssueCommand(context.Background(), &commandv1.IssueCommandRequest{
			DeviceId:    "device-1",
			CommandType: "reboot",
			Payload:     []byte("{}"),
		})
		if err != nil {
			t.Fatalf("IssueCommand: unexpected error: %v", err)
		}
		if resp.GetAckId() == "" {
			t.Error("ack_id must be non-empty")
		}
		if got := resp.GetAcknowledgedAtUnixNano(); got != fixedTime.UnixNano() {
			t.Errorf("acknowledged_at_unix_nano = %d, want %d (injected clock)", got, fixedTime.UnixNano())
		}
	})

	cases := []struct {
		name string
		req  *commandv1.IssueCommandRequest
	}{
		{"empty device_id", &commandv1.IssueCommandRequest{CommandType: "reboot"}},
		{"empty command_type", &commandv1.IssueCommandRequest{DeviceId: "device-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := srv.IssueCommand(context.Background(), tc.req)
			if err == nil {
				t.Fatalf("expected error for %s, got resp=%v", tc.name, resp)
			}
			var ce *errcode.Error
			if !errors.As(err, &ce) {
				t.Fatalf("error is not *errcode.Error: %v", err)
			}
			if ce.Code != errcode.ErrValidationFailed {
				t.Errorf("Code = %q, want %q", ce.Code, errcode.ErrValidationFailed)
			}
		})
	}
}

// TestServer_IssueCommand_OverGRPC is the end-to-end acid test (#1151): a real
// gRPC client dials the server over an in-process bufconn, the handler is
// registered via the buf-generated RegisterDeviceCommandServiceServer (the same
// call cellgen emits into cell_gen.go), and the unary RPC round-trips. The error
// path returns a non-OK status (the precise errcode→codes mapping lands in PR-12;
// here we assert it is simply not OK).
func TestServer_IssueCommand_OverGRPC(t *testing.T) {
	t.Parallel()
	const bufSize = 1024 * 1024
	lis := bufconn.Listen(bufSize)
	grpcServer := grpc.NewServer()
	commandv1.RegisterDeviceCommandServiceServer(grpcServer, newTestServer())

	serveErr := make(chan error, 1)
	go func() { serveErr <- grpcServer.Serve(lis) }()
	t.Cleanup(func() {
		grpcServer.GracefulStop()
		if err := <-serveErr; err != nil {
			t.Errorf("grpc Serve: %v", err)
		}
	})

	conn, err := grpc.NewClient(
		"passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := commandv1.NewDeviceCommandServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	t.Run("success", func(t *testing.T) {
		resp, err := client.IssueCommand(ctx, &commandv1.IssueCommandRequest{
			DeviceId:    "device-1",
			CommandType: "reboot",
		})
		if err != nil {
			t.Fatalf("IssueCommand over grpc: %v", err)
		}
		if resp.GetAckId() == "" {
			t.Error("ack_id must be non-empty")
		}
		if resp.GetAcknowledgedAtUnixNano() != fixedTime.UnixNano() {
			t.Errorf("acknowledged_at_unix_nano = %d, want %d", resp.GetAcknowledgedAtUnixNano(), fixedTime.UnixNano())
		}
	})

	t.Run("error path is non-OK status", func(t *testing.T) {
		_, err := client.IssueCommand(ctx, &commandv1.IssueCommandRequest{CommandType: "reboot"})
		if err == nil {
			t.Fatal("expected non-nil error for missing device_id")
		}
		if status.Code(err) == codes.OK {
			t.Errorf("status code = OK, want non-OK; err=%v", err)
		}
	})
}
