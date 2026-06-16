package devicecommandrpc

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecmd"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/domain"
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/mem"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/command/commandtest"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	commandv1 "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1"
)

// These tests cover the gRPC command handler's DOMAIN behavior only (validate +
// enqueue + stream). Authorization (device:command) is NOT a handler concern as of
// #2008: the runtime gRPC auth interceptor runs the ABAC PDP gate before the
// handler is invoked (declared in endpoints.grpc.methods[].permission). The gate's
// allow/deny/no-mapping behavior is covered at the interceptor layer
// (framework/runtime/grpc/interceptor), so the handler tests no longer inject a
// principal or assert authorization — they assume an already-authorized caller.

var fixedTime = time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC)

const seededDeviceID = "device-1"

// newTestServer builds a Server backed by in-memory persistence with one
// pre-seeded device ("device-1") so the enqueue path succeeds. The handler holds
// no Authorizer (#2008) — authorization is enforced by the interceptor.
func newTestServer(t *testing.T) *Server {
	t.Helper()
	devRepo := mem.NewDeviceRepository()
	q := commandtest.NewInMemQueue()
	codec, err := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatalf("cursor codec: %v", err)
	}
	svc, err := devicecmd.NewService(
		clockmock.New(fixedTime), q, devRepo, codec, slog.Default(), query.RunModeProd,
		devicecmd.WithSliceName("devicecommandrpc"),
	)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if err := devRepo.Create(context.Background(), &domain.Device{ID: seededDeviceID, Name: "sensor-a", Status: "online"}); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	return NewServer(clockmock.New(fixedTime), svc)
}

// TestServer_IssueCommand_Direct exercises the handler in-process (no transport):
// the success path enqueues a command and returns its real id; each missing-field
// path returns an *errcode.Error (KindInvalid / ErrValidationFailed).
func TestServer_IssueCommand_Direct(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t)

	t.Run("success enqueues and returns the command id", func(t *testing.T) {
		assertIssueCommandSuccess(t, srv)
	})

	cases := []struct {
		name string
		req  *commandv1.IssueCommandRequest
	}{
		{"empty device_id", &commandv1.IssueCommandRequest{CommandType: "reboot", Payload: []byte("{}")}},
		{"empty command_type", &commandv1.IssueCommandRequest{DeviceId: seededDeviceID, Payload: []byte("{}")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertIssueCommandValidationError(t, srv, tc.name, tc.req)
		})
	}
}

func assertIssueCommandSuccess(t *testing.T, srv *Server) {
	t.Helper()

	resp, err := srv.IssueCommand(context.Background(), &commandv1.IssueCommandRequest{
		DeviceId:    seededDeviceID,
		CommandType: "reboot",
		Payload:     []byte("{}"),
	})
	if err != nil {
		t.Fatalf("IssueCommand: unexpected error: %v", err)
	}
	// ack_id carries the real enqueued command id (cmd-<hex>), not a throwaway uuid.
	if !strings.HasPrefix(resp.GetAckId(), "cmd-") {
		t.Errorf("ack_id = %q, want a cmd- prefixed enqueued command id", resp.GetAckId())
	}
	if got := resp.GetAcknowledgedAtUnixNano(); got != fixedTime.UnixNano() {
		t.Errorf("acknowledged_at_unix_nano = %d, want %d (injected clock)", got, fixedTime.UnixNano())
	}
}

func assertIssueCommandValidationError(t *testing.T, srv *Server, name string, req *commandv1.IssueCommandRequest) {
	t.Helper()

	resp, err := srv.IssueCommand(context.Background(), req)
	if err == nil {
		t.Fatalf("expected error for %s, got resp=%v", name, resp)
	}
	var ce *errcode.Error
	if !errors.As(err, &ce) {
		t.Fatalf("error is not *errcode.Error: %v", err)
	}
	if ce.Code != errcode.ErrValidationFailed {
		t.Errorf("Code = %q, want %q", ce.Code, errcode.ErrValidationFailed)
	}
}

// TestServer_IssueCommand_OverGRPC is the end-to-end acid test (#1151): a real
// gRPC client dials the server over an in-process bufconn, the handler is
// registered via the buf-generated RegisterDeviceCommandServiceServer (the same
// call cellgen emits into cell_gen.go), and the unary RPC round-trips. No auth
// interceptor is wired here — authorization is exercised at the interceptor layer
// (#2008); this test asserts the domain round-trip. The error path returns a
// non-OK status (the precise errcode→codes mapping lands in PR-12).
func TestServer_IssueCommand_OverGRPC(t *testing.T) {
	t.Parallel()
	const bufSize = 1024 * 1024
	lis := bufconn.Listen(bufSize)
	grpcServer := grpc.NewServer()
	commandv1.RegisterDeviceCommandServiceServer(grpcServer, newTestServer(t))

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
	ctx, cancel := context.WithTimeout(context.Background(), testtime.EventuallyLong)
	defer cancel()

	t.Run("success", func(t *testing.T) {
		resp, err := client.IssueCommand(ctx, &commandv1.IssueCommandRequest{
			DeviceId:    seededDeviceID,
			CommandType: "reboot",
			Payload:     []byte("{}"),
		})
		if err != nil {
			t.Fatalf("IssueCommand over grpc: %v", err)
		}
		if !strings.HasPrefix(resp.GetAckId(), "cmd-") {
			t.Errorf("ack_id = %q, want enqueued command id", resp.GetAckId())
		}
		if resp.GetAcknowledgedAtUnixNano() != fixedTime.UnixNano() {
			t.Errorf("acknowledged_at_unix_nano = %d, want %d", resp.GetAcknowledgedAtUnixNano(), fixedTime.UnixNano())
		}
	})

	// Both validation branches over the wire return a non-OK status. We assert
	// only "not OK" (not the precise code): a returned *errcode.Error currently
	// surfaces as codes.Unknown — the errcode→codes.Code mapping table lands in
	// PR-12 (the Kratos GRPCStatus() model), at which point these become
	// codes.InvalidArgument.
	errCases := []struct {
		name string
		req  *commandv1.IssueCommandRequest
	}{
		{"missing device_id", &commandv1.IssueCommandRequest{CommandType: "reboot", Payload: []byte("{}")}},
		{"missing command_type", &commandv1.IssueCommandRequest{DeviceId: seededDeviceID, Payload: []byte("{}")}},
	}
	for _, ec := range errCases {
		t.Run("error path is non-OK status: "+ec.name, func(t *testing.T) {
			if _, err := client.IssueCommand(ctx, ec.req); err == nil {
				t.Fatalf("expected non-nil error for %s", ec.name)
			} else if status.Code(err) == codes.OK {
				t.Errorf("status code = OK, want non-OK; err=%v", err)
			}
		})
	}
}
