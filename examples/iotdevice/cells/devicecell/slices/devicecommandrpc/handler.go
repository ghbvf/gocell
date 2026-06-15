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
	"github.com/ghbvf/gocell/framework/pkg/query"
	commandv1 "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1"
)

// watchSnapshotLimit bounds the initial active-command snapshot WatchCommands
// streams before it tails for drain/disconnect. The snapshot is silently capped
// at this many entries — a production watch should paginate (check page.HasMore
// and stream subsequent pages) rather than truncate.
const watchSnapshotLimit = 100

// Server implements commandv1.DeviceCommandServiceServer.
type Server struct {
	commandv1.UnimplementedDeviceCommandServiceServer
	clk    clock.Clock
	cmdSvc *devicecmd.Service
}

// NewServer constructs the gRPC command server. clock is a mandatory positional
// dependency (CLOCK-POSITIONAL-INJECTION-01); the acknowledgement timestamp is
// stamped from it so it stays consistent with the cell's business clock. cmdSvc
// is the shared device-command domain service — the gRPC handler enqueues
// through the same Enqueue path the HTTP devicecommand slice uses.
//
// Authorization is NOT a handler concern: the runtime gRPC auth interceptor runs
// the ABAC PDP gate for device:command before the handler is invoked (#2008,
// declared in endpoints.grpc.methods[].permission). The handler therefore holds no
// Authorizer — the per-method permission gate is enforced transport-side, mirroring
// the HTTP RequirePermission route gate.
func NewServer(clk clock.Clock, cmdSvc *devicecmd.Service) *Server {
	clock.MustHaveClock(clk, "devicecommandrpc.NewServer")
	return &Server{clk: clk, cmdSvc: cmdSvc}
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

// WatchCommands validates the request, streams the device's currently active
// commands as a snapshot (reusing the same ScanActive domain read the HTTP list
// path uses), then keeps the watch open until the caller disconnects or the server
// drains. It is the example's first server-streaming RPC (PR-10 #1153).
//
// Authorization (device:command) is enforced by the runtime gRPC auth interceptor
// at stream-open, BEFORE this handler runs (#2008) — the same gate as IssueCommand,
// declared once in endpoints.grpc.methods[].permission. WatchCommands and
// IssueCommand intentionally share device:command (admin/operator coarse gate).
// Granting devices the ability to watch their own command queue via gRPC
// (device:consume semantic) is a distinct enhancement, out of scope here.
//
// Drain discipline: the snapshot loop checks stream.Context().Err() between
// sends and the tail blocks on stream.Context().Done(), so the framework drain
// signal (StreamDrain cancels the stream context at GracefulStop) terminates an
// in-flight watch promptly instead of holding the graceful-stop budget. A
// production watch would push newly-enqueued commands during the tail; the
// example demonstrates the long-lived server-stream shape and the framework drain.
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
	page, err := s.cmdSvc.ScanActive(ctx,
		command.ScanFilter{DeviceID: req.GetDeviceId()},
		query.PageParams{Limit: watchSnapshotLimit})
	if err != nil {
		return err
	}
	for i := range page.Items {
		if err := ctx.Err(); err != nil {
			return err // drain discipline: stop the moment the stream ctx is canceled
		}
		e := page.Items[i]
		if err := stream.Send(&commandv1.WatchCommandsResponse{
			CommandId:   e.ID,
			CommandType: e.CommandType,
			Status:      e.Status.String(),
		}); err != nil {
			return err
		}
	}
	// Tail until the caller disconnects or the server drains (StreamDrain cancels
	// ctx on GracefulStop).
	<-ctx.Done()
	return ctx.Err()
}
