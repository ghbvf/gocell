package devicecommandrpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"

	commandv1 "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1"
	"github.com/ghbvf/gocell/pkg/errcode"
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
