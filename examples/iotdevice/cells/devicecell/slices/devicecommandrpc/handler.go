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
	"github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/dto"
	commandv1 "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

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
func NewServer(clk clock.Clock, cmdSvc *devicecmd.Service) *Server {
	clock.MustHaveClock(clk, "devicecommandrpc.NewServer")
	return &Server{clk: clk, cmdSvc: cmdSvc}
}

// IssueCommand authorizes the caller, validates the request, enqueues the
// command into the L4 device command queue, and returns the enqueued command id
// as the acknowledgement.
//
// Authorization mirrors the HTTP devicecommand enqueue route policy
// (auth.AnyRole(admin, operator)). gRPC has no route-policy layer, so the role
// gate runs at the handler edge, before any field validation or device lookup,
// preserving the 403-before-404 ordering so an unauthorized caller cannot probe
// device existence (per-method gRPC auth is #1675). A domain error is returned
// as an *errcode.Error; the gRPC interceptor chain maps it to a status code (the
// full errcode→codes table is PR-12, the Kratos GRPCStatus() model).
func (s *Server) IssueCommand(
	ctx context.Context,
	req *commandv1.IssueCommandRequest,
) (*commandv1.IssueCommandResponse, error) {
	if p, ok := auth.FromContext(ctx); !ok ||
		(!p.HasRole(dto.RoleAdmin) && !p.HasRole(dto.RoleOperator)) {
		return nil, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden,
			"device-command: requires admin or operator role")
	}
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
