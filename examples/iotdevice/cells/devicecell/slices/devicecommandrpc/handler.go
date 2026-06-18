// Package devicecommandrpc implements the gRPC server for the
// grpc.device.command.v1 contract — the iotdevice example's first end-to-end
// unary RPC (#1151). It is the grpc-serve counterpart of the HTTP devicecommand
// slice: a control-plane caller issues a command to a device over gRPC and the
// handler enqueues it into the L4 device command queue — reusing the same
// devicecmd.Service.Enqueue domain path as the HTTP enqueue slice — then returns
// the enqueued command id as the acknowledgement.
//
// The server contract is buf's generated commandv1.DeviceCommandServiceServer
// interface — contractgen emits no Go for kind=grpc (#1688). Server embeds
// commandv1.UnimplementedDeviceCommandServiceServer (by value) for forward
// compatibility, which is also what satisfies the pb interface so that the
// cellgen-generated reg.GRPCService(... pb.RegisterDeviceCommandServiceServer ...)
// call compiles.
package devicecommandrpc

import (
	"context"
	"log/slog"

	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/devicecmd"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/command"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
	"github.com/ghbvf/gocell/framework/pkg/query"
	commandv1 "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1"
)

// watchSnapshotPageSize is the per-page size WatchCommands uses to page through
// the device's full active-command snapshot before it tails for new commands. The
// snapshot is NOT truncated: streamSnapshot loops over every page (cursor
// pagination) until the backend reports no more, so a device with more than one
// page of active commands still receives all of them at watch-open time.
const watchSnapshotPageSize = 100

// Server implements commandv1.DeviceCommandServiceServer.
type Server struct {
	commandv1.UnimplementedDeviceCommandServiceServer
	clk      clock.Clock
	cmdSvc   *devicecmd.Service
	notifier *devicecmd.Notifier
	// subscribeHook, if non-nil, is called in WatchCommands immediately after
	// Subscribe returns. Test-only seam for synchronizing Subscribe-before-enqueue
	// ordering in latent-delivery tests; always nil in production.
	subscribeHook func(deviceID string)
}

// NewServer constructs the gRPC command server. clock is a mandatory positional
// dependency (CLOCK-POSITIONAL-INJECTION-01); the acknowledgement timestamp is
// stamped from it so it stays consistent with the cell's business clock. cmdSvc
// is the shared device-command domain service — the gRPC handler enqueues
// through the same Enqueue path the HTTP devicecommand slice uses. notifier is
// the shared in-process watcher hub (#1795): WatchCommands subscribes through it
// so devices receive newly-enqueued commands in real time; it must be the same
// instance injected into all enqueue-capable Service instances via WithOnEnqueue.
//
// Authorization is NOT a handler concern: the runtime gRPC auth interceptor runs
// the ABAC PDP gate for device:command before the handler is invoked (#2008,
// declared in endpoints.grpc.methods[].permission). The handler therefore holds no
// Authorizer — the per-method permission gate is enforced transport-side, mirroring
// the HTTP RequirePermission route gate.
func NewServer(clk clock.Clock, cmdSvc *devicecmd.Service, notifier *devicecmd.Notifier) *Server {
	clock.MustHaveClock(clk, "devicecommandrpc.NewServer")
	if cmdSvc == nil {
		panic(panicregister.Approved("devicecommandrpc-server-cmdsvc-required",
			errcode.Assertion("devicecommandrpc.NewServer: cmdSvc must not be nil")))
	}
	if notifier == nil {
		panic(panicregister.Approved("devicecommandrpc-server-notifier-required",
			errcode.Assertion("devicecommandrpc.NewServer: notifier must not be nil")))
	}
	return &Server{clk: clk, cmdSvc: cmdSvc, notifier: notifier}
}

// IssueCommand validates the request, enqueues the command into the L4 device
// command queue, and returns the enqueued command id as the acknowledgement.
//
// Authorization (device:command) is enforced by the runtime gRPC auth interceptor
// BEFORE this handler runs (#2008, contract overlay endpoints.grpc.methods[].permission),
// matching the HTTP devicecommand enqueue route gate (RequirePermission(PermDeviceCommand)).
// The interceptor gate runs before any field validation, preserving the
// 403-before-404 ordering so an unauthorized caller cannot probe device existence.
// A domain error is returned as an *errcode.Error; the gRPC interceptor chain maps
// it to a status code.
func (s *Server) IssueCommand(
	ctx context.Context,
	req *commandv1.IssueCommandRequest,
) (*commandv1.IssueCommandResponse, error) {
	if req.GetDeviceId() == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"device_id is required",
			errcode.WithDetails(errcode.PublicString("field", "device_id")))
	}
	if req.GetCommandType() == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command_type is required",
			errcode.WithDetails(errcode.PublicString("field", "command_type")))
	}
	entry, err := s.cmdSvc.Enqueue(ctx, req.GetDeviceId(), req.GetCommandType(), string(req.GetPayload()))
	if err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "devicecommandrpc: command enqueued",
		slog.String("device_id", entry.DeviceID),
		slog.String("command_type", entry.CommandType),
		slog.String("command_id", entry.ID))
	return &commandv1.IssueCommandResponse{
		AckId:                  entry.ID,
		AcknowledgedAtUnixNano: s.clk.Now().UnixNano(),
	}, nil
}

// toWatchResponse converts a command.Entry to a WatchCommandsResponse wire
// message. Used for both the on-connect snapshot and the real-time tail so the
// field mapping is defined once. The response carries only the command's
// notification identity (id / type / status) — it is a real-time DOORBELL, not a
// consume payload: the device claims the command and reads its payload/attempt by
// calling the HTTP Dequeue path (which leases it), so this stream stays read-only.
func toWatchResponse(e command.Entry) *commandv1.WatchCommandsResponse {
	return &commandv1.WatchCommandsResponse{
		CommandId:   e.ID,
		CommandType: e.CommandType,
		Status:      e.Status.String(),
	}
}

// WatchCommands is a real-time NOTIFICATION stream (a doorbell), not a consume
// stream: it pages through the device's currently-active commands as an initial
// snapshot (reusing the same ScanActive domain read the HTTP list path uses), then
// tails newly-enqueued commands in real time until the caller disconnects or the
// server drains (#1795). The device claims + executes each command via the HTTP
// Dequeue path (which leases it and returns the payload/attempt); this stream only
// signals which commands are waiting.
//
// Subscribe-before-snapshot ordering: the notifier subscription is established
// BEFORE the ScanActive snapshot so that commands enqueued in the window between the
// snapshot read and the tail-start are not silently lost. The snapshot is
// authoritative for "active at open time"; the tail delivers everything enqueued
// after that point. Best-effort delivery: if a subscriber's buffer is full the
// Notifier drops — the device must reconnect to resync.
//
// Authorization (device:consume, owner-scoped) is enforced by the runtime gRPC auth
// interceptor BEFORE this handler runs (#2008/#2207). The contract declares
// resource: device_id, so the interceptor extracts the per-message device_id and
// forwards it to the PDP: admin/operator pass coarsely, and the device itself passes
// when subject == device_id, letting a device watch its OWN queue. device:consume is
// the consume-lifecycle umbrella (the same gate the HTTP dequeue/ack/report path
// uses); this stream is its real-time notification arm. The handler does not
// hand-authorize.
//
// Drain discipline: the snapshot loop checks stream.Context().Err() between
// sends and the tail select includes ctx.Done(), so the framework drain signal
// (StreamDrain cancels the stream context at GracefulStop) terminates an
// in-flight watch promptly instead of holding the graceful-stop budget.
func (s *Server) WatchCommands(
	req *commandv1.WatchCommandsRequest,
	stream commandv1.DeviceCommandService_WatchCommandsServer,
) error {
	ctx := stream.Context()
	if req.GetDeviceId() == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"device_id is required",
			errcode.WithDetails(errcode.PublicString("field", "device_id")))
	}

	// Subscribe BEFORE the snapshot to avoid the snapshot↔tail gap (#1795).
	ch, cancel := s.notifier.Subscribe(req.GetDeviceId())
	defer cancel()
	if s.subscribeHook != nil {
		s.subscribeHook(req.GetDeviceId())
	}

	// Snapshot: page through ALL currently-active commands (no silent truncation)
	// before tailing — a device with more than one page still gets its full backlog.
	if err := streamSnapshot(ctx, s.cmdSvc, req.GetDeviceId(), stream); err != nil {
		return err
	}

	// Tail: deliver newly-enqueued commands until the caller disconnects or the
	// server drains (StreamDrain cancels ctx on GracefulStop).
	return tailWatchStream(ctx, ch, stream)
}

// streamSnapshot pages through the device's entire active-command set via cursor
// pagination and sends each as a snapshot notification. It loops until the backend
// reports no further pages, so the snapshot is never silently truncated (the device
// receives every active command at watch-open time, not just the first page). The
// per-item ctx.Err() check preserves drain discipline mid-snapshot.
func streamSnapshot(
	ctx context.Context,
	cmdSvc *devicecmd.Service,
	deviceID string,
	stream commandv1.DeviceCommandService_WatchCommandsServer,
) error {
	cursor := ""
	for {
		if err := ctx.Err(); err != nil {
			return err // drain discipline: stop the moment the stream ctx is canceled
		}
		page, err := cmdSvc.ScanActive(ctx,
			command.ScanFilter{DeviceID: deviceID},
			query.PageParams{Limit: watchSnapshotPageSize, Cursor: cursor})
		if err != nil {
			return err
		}
		for i := range page.Items {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := stream.Send(toWatchResponse(page.Items[i])); err != nil {
				return err
			}
		}
		if !page.HasMore {
			return nil
		}
		cursor = page.NextCursor
	}
}

// tailWatchStream drives the real-time tail of WatchCommands: it blocks on ch
// (the Notifier subscription) and ctx.Done(), forwarding each new entry to the
// stream. Extracted to keep WatchCommands under the cognitive-complexity limit.
func tailWatchStream(
	ctx context.Context,
	ch <-chan command.Entry,
	stream commandv1.DeviceCommandService_WatchCommandsServer,
) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e, ok := <-ch:
			if !ok {
				// Safety net: the Notifier never closes the channel (see notifier.go
				// "channel is never closed by the Notifier" doc) — this branch is
				// unreachable in practice but guards against future refactoring.
				return ctx.Err()
			}
			if err := stream.Send(toWatchResponse(e)); err != nil {
				return err
			}
		}
	}
}
