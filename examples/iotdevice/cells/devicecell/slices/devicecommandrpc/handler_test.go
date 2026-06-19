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
	"github.com/ghbvf/gocell/framework/runtime/grpc/interceptor"
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
// A real Notifier is injected for the real-time push path (#1795).
func newTestServer(t *testing.T) *Server {
	t.Helper()
	return newTestServerWithNotifier(t, devicecmd.NewNotifier())
}

// newTestServerWithNotifier builds a Server with the given Notifier, useful in
// tests that need to drive latent delivery by also wiring the notifier into
// the Service's WithOnEnqueue hook.
func newTestServerWithNotifier(t *testing.T, notifier *devicecmd.Notifier) *Server {
	t.Helper()
	return newTestServerWith(t, notifier)
}

// newTestServerWith builds a Server backed by in-memory persistence with one
// pre-seeded device ("device-1"), applying extra Service options on top of the
// shared slice-name + notifier wiring. Lets a test vary the per-device Pending
// limit (devicecmd.WithPendingLimit) without copying the construction boilerplate.
func newTestServerWith(t *testing.T, notifier *devicecmd.Notifier, extra ...devicecmd.Option) *Server {
	t.Helper()
	devRepo := mem.NewDeviceRepository()
	q := commandtest.NewInMemQueue()
	codec, err := query.NewCursorCodec(bytes.Repeat([]byte("k"), 32))
	if err != nil {
		t.Fatalf("cursor codec: %v", err)
	}
	opts := append([]devicecmd.Option{
		devicecmd.WithSliceName("devicecommandrpc"),
		devicecmd.WithOnEnqueue(notifier.Notify),
	}, extra...)
	svc, err := devicecmd.NewService(
		clockmock.New(fixedTime), q, devRepo, codec, slog.Default(), query.RunModeProd, opts...)
	if err != nil {
		t.Fatalf("new service: %v", err)
	}
	if err := devRepo.Create(context.Background(), &domain.Device{ID: seededDeviceID, Name: "sensor-a", Status: "online"}); err != nil {
		t.Fatalf("seed device: %v", err)
	}
	return NewServer(clockmock.New(fixedTime), svc, notifier)
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
// (#2008); this test asserts the domain round-trip. dialInProcess wires
// UnaryErrcodeMap (PR-12 #1155) so the error path returns the mapped codes.Code
// (KindInvalid → InvalidArgument).
func TestServer_IssueCommand_OverGRPC(t *testing.T) {
	t.Parallel()
	client := dialInProcess(t, newTestServer(t))
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

	// Both validation branches over the wire return codes.InvalidArgument: the
	// handler returns *errcode.Error(KindInvalid), and the UnaryErrcodeMap
	// interceptor (PR-12 #1155, wired into dialInProcess) maps KindInvalid →
	// codes.InvalidArgument. The per-device cap (KindRateLimited) maps to
	// codes.ResourceExhausted through the same central interceptor — asserted by
	// TestServer_IssueCommand_OverCap_ResourceExhausted.
	errCases := []struct {
		name string
		req  *commandv1.IssueCommandRequest
	}{
		{"missing device_id", &commandv1.IssueCommandRequest{CommandType: "reboot", Payload: []byte("{}")}},
		{"missing command_type", &commandv1.IssueCommandRequest{DeviceId: seededDeviceID, Payload: []byte("{}")}},
	}
	for _, ec := range errCases {
		t.Run("validation error is InvalidArgument: "+ec.name, func(t *testing.T) {
			_, err := client.IssueCommand(ctx, ec.req)
			if err == nil {
				t.Fatalf("expected non-nil error for %s", ec.name)
			}
			if got := status.Code(err); got != codes.InvalidArgument {
				t.Errorf("status code = %v, want InvalidArgument; err=%v", got, err)
			}
		})
	}
}

// TestServer_IssueCommand_OverCap_ResourceExhausted asserts the per-device
// Pending cap (F-S-005 #822) surfaces over the gRPC wire as the standard
// codes.ResourceExhausted, not the codes.Unknown an unmapped *errcode.Error
// would yield. The handler returns the raw errcode.KindRateLimited error; the
// central UnaryErrcodeMap interceptor (PR-12 #1155, wired into dialInProcess)
// maps it to codes.ResourceExhausted — the interim in-handler projection that
// #2444 added pending this mapper has been removed.
func TestServer_IssueCommand_OverCap_ResourceExhausted(t *testing.T) {
	t.Parallel()
	// Limit 1: the first enqueue fills the device's Pending cap, the second exceeds it.
	client := dialInProcess(t, newTestServerWith(t, devicecmd.NewNotifier(), devicecmd.WithPendingLimit(1)))
	ctx, cancel := context.WithTimeout(context.Background(), testtime.EventuallyLong)
	defer cancel()

	req := &commandv1.IssueCommandRequest{DeviceId: seededDeviceID, CommandType: "reboot", Payload: []byte("{}")}
	firstResp, err := client.IssueCommand(ctx, req)
	if err != nil {
		t.Fatalf("first IssueCommand (fills cap): unexpected error: %v", err)
	}
	if !strings.HasPrefix(firstResp.GetAckId(), "cmd-") {
		t.Errorf("first IssueCommand ack_id = %q, want a cmd- prefixed enqueued command id", firstResp.GetAckId())
	}
	if got := firstResp.GetAcknowledgedAtUnixNano(); got != fixedTime.UnixNano() {
		t.Errorf("first IssueCommand acknowledged_at_unix_nano = %d, want %d (injected clock)", got, fixedTime.UnixNano())
	}
	_, err = client.IssueCommand(ctx, req)
	if err == nil {
		t.Fatalf("expected over-cap error on second IssueCommand, got nil")
	}
	if got := status.Code(err); got != codes.ResourceExhausted {
		t.Errorf("status code = %v, want ResourceExhausted; err=%v", got, err)
	}
}

// dialInProcess starts srv on an in-process bufconn listener and returns a
// connected client. Server graceful-stop and connection teardown are registered
// via t.Cleanup, so callers just dial and issue. Shared by the over-the-wire
// gRPC tests so the bufconn boilerplate lives in one place.
func dialInProcess(t *testing.T, srv *Server) commandv1.DeviceCommandServiceClient {
	t.Helper()
	const bufSize = 1024 * 1024
	lis := bufconn.Listen(bufSize)
	// Wire UnaryErrcodeMap (PR-12 #1155) so a handler-returned *errcode.Error is
	// projected to the mapped codes.Code over the real gRPC transport — the same
	// centralized mapping production uses, replacing per-handler status.Error
	// wrapping. KindInvalid → InvalidArgument, KindRateLimited → ResourceExhausted.
	grpcServer := grpc.NewServer(
		grpc.UnaryInterceptor(interceptor.UnaryErrcodeMap()),
	)
	commandv1.RegisterDeviceCommandServiceServer(grpcServer, srv)

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
	return commandv1.NewDeviceCommandServiceClient(conn)
}
