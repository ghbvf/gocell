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

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	commandv1 "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1"
)

// watchTestTimeout bounds the WatchCommands snapshot/tail waits (TEST-TIME-LITERAL-01).
const watchTestTimeout = 2 * time.Second

// fakeWatchStream is a minimal commandv1.DeviceCommandService_WatchCommandsServer
// (grpc.ServerStreamingServer[WatchCommandsResponse]). Embedding the nil
// grpc.ServerStream satisfies the rest of the method set; the handler only uses
// Context() and Send(). Sent events go to a buffered channel so the test can
// observe the snapshot without racing the handler goroutine.
type fakeWatchStream struct {
	grpc.ServerStream
	ctx    context.Context
	sentCh chan *commandv1.WatchCommandsResponse
}

func (f *fakeWatchStream) Context() context.Context { return f.ctx }
func (f *fakeWatchStream) Send(e *commandv1.WatchCommandsResponse) error {
	f.sentCh <- e
	return nil
}

func newWatchStream(ctx context.Context) *fakeWatchStream {
	return &fakeWatchStream{ctx: ctx, sentCh: make(chan *commandv1.WatchCommandsResponse, 8)}
}

// TestServer_WatchCommands_Unauthorized asserts the role gate runs at the handler
// edge before any work: a stream with no principal is denied ErrAuthForbidden.
func TestServer_WatchCommands_Unauthorized(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t)
	err := srv.WatchCommands(&commandv1.WatchCommandsRequest{DeviceId: seededDeviceID}, newWatchStream(context.Background()))
	var ce *errcode.Error
	if !errors.As(err, &ce) || ce.Code != errcode.ErrAuthForbidden {
		t.Fatalf("want ErrAuthForbidden, got %v", err)
	}
}

// TestServer_WatchCommands_EmptyDeviceID asserts device_id is validated.
func TestServer_WatchCommands_EmptyDeviceID(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t)
	err := srv.WatchCommands(&commandv1.WatchCommandsRequest{}, newWatchStream(operatorCtx(context.Background())))
	var ce *errcode.Error
	if !errors.As(err, &ce) || ce.Code != errcode.ErrValidationFailed {
		t.Fatalf("want ErrValidationFailed, got %v", err)
	}
}

// TestServer_WatchCommands_SnapshotThenTailUntilCancel exercises the server-stream
// shape: the handler streams the device's active command snapshot, then tails
// until its context is canceled (the drain discipline StreamDrain relies on —
// here the cancel stands in for the framework drain signal). The handler must
// return ctx.Err() promptly once canceled, not hang.
func TestServer_WatchCommands_SnapshotThenTailUntilCancel(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t)

	// Seed one active command for the device via the same enqueue path.
	if _, err := srv.IssueCommand(operatorCtx(context.Background()), &commandv1.IssueCommandRequest{
		DeviceId:    seededDeviceID,
		CommandType: "reboot",
		Payload:     []byte("{}"),
	}); err != nil {
		t.Fatalf("seed command: %v", err)
	}

	ctx, cancel := context.WithCancel(operatorCtx(context.Background()))
	stream := newWatchStream(ctx)
	done := make(chan error, 1)
	go func() {
		done <- srv.WatchCommands(&commandv1.WatchCommandsRequest{DeviceId: seededDeviceID}, stream)
	}()

	// Snapshot: the seeded active command is streamed.
	select {
	case e := <-stream.sentCh:
		if e.GetCommandType() != "reboot" {
			t.Errorf("snapshot event command_type = %q, want reboot", e.GetCommandType())
		}
		if e.GetCommandId() == "" {
			t.Errorf("snapshot event must carry the command id")
		}
	case <-time.After(watchTestTimeout):
		t.Fatalf("WatchCommands did not stream the active-command snapshot")
	}

	// Tail: the handler is now blocked on ctx.Done(); canceling must return it.
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("WatchCommands tail must return context.Canceled on cancel, got %v", err)
		}
	case <-time.After(watchTestTimeout):
		t.Fatalf("WatchCommands tail did not return after context cancel (drain discipline broken)")
	}
}

// principalServerStream injects an operator principal into the stream context so
// the handler's role gate passes (the gRPC analog of authInject in the
// IssueCommand over-gRPC test; production uses interceptor.StreamAuth).
type principalServerStream struct {
	grpc.ServerStream
	ctx context.Context
}

func (s *principalServerStream) Context() context.Context { return s.ctx }

// TestServer_WatchCommands_OverGRPC drives the server-streaming RPC end-to-end
// over an in-process bufconn: the handler is registered via the buf-generated
// RegisterDeviceCommandServiceServer (the same call cellgen emits), a real
// grpc.ServerStream carries the snapshot, and the generated client streams it
// back. It asserts the seeded active command arrives in the snapshot and that a
// client-side cancel ends the stream (the drain/disconnect discipline over real
// transport, which the fake-stream tests cannot exercise).
func TestServer_WatchCommands_OverGRPC(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t)
	if _, err := srv.IssueCommand(operatorCtx(context.Background()), &commandv1.IssueCommandRequest{
		DeviceId:    seededDeviceID,
		CommandType: "reboot",
		Payload:     []byte("{}"),
	}); err != nil {
		t.Fatalf("seed command: %v", err)
	}

	lis := bufconn.Listen(1024 * 1024)
	streamAuthInject := func(s any, ss grpc.ServerStream, _ *grpc.StreamServerInfo, h grpc.StreamHandler) error {
		return h(s, &principalServerStream{ServerStream: ss, ctx: operatorCtx(ss.Context())})
	}
	grpcServer := grpc.NewServer(grpc.StreamInterceptor(streamAuthInject))
	commandv1.RegisterDeviceCommandServiceServer(grpcServer, srv)
	serveErr := make(chan error, 1)
	go func() { serveErr <- grpcServer.Serve(lis) }()
	t.Cleanup(func() {
		grpcServer.GracefulStop()
		if err := <-serveErr; err != nil {
			t.Errorf("grpc Serve: %v", err)
		}
	})

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(_ context.Context, _ string) (net.Conn, error) { return lis.Dial() }),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("grpc.NewClient: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	client := commandv1.NewDeviceCommandServiceClient(conn)
	ctx, cancel := context.WithTimeout(context.Background(), watchTestTimeout)
	defer cancel()
	stream, err := client.WatchCommands(ctx, &commandv1.WatchCommandsRequest{DeviceId: seededDeviceID})
	if err != nil {
		t.Fatalf("WatchCommands: %v", err)
	}

	// Snapshot: the seeded active command is streamed over the wire.
	resp, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv snapshot: %v", err)
	}
	if resp.GetCommandType() != "reboot" || resp.GetCommandId() == "" {
		t.Errorf("snapshot event = %+v, want a reboot command with an id", resp)
	}

	// Tail: cancel the client RPC → the handler's stream ctx is canceled → handler
	// returns → the next Recv ends with an error (drain/disconnect over transport).
	cancel()
	if _, err := stream.Recv(); err == nil {
		t.Fatalf("Recv after client cancel must return an error (stream ended)")
	} else if c := status.Code(err); c != codes.Canceled {
		// Canceled is expected; a connection-close race may surface Unavailable.
		t.Logf("Recv-after-cancel code = %v (Canceled expected)", c)
	}
}
