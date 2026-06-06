// Package devicecommandrpc implements the gRPC server for the
// grpc.device.command.v1 contract — the iotdevice example's first end-to-end
// unary RPC (#1151). It is the grpc-serve counterpart of the HTTP devicecommand
// slice: a control-plane caller issues a command to a device over gRPC and
// receives an acknowledgement.
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

	"github.com/google/uuid"

	commandv1 "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Server implements commandv1.DeviceCommandServiceServer.
type Server struct {
	commandv1.UnimplementedDeviceCommandServiceServer
	clk clock.Clock
}

// NewServer constructs the gRPC command server. clock is a mandatory positional
// dependency (CLOCK-POSITIONAL-INJECTION-01); the acknowledgement timestamp is
// stamped from it so it stays consistent with the cell's business clock.
func NewServer(clk clock.Clock) *Server {
	clock.MustHaveClock(clk, "devicecommandrpc.NewServer")
	return &Server{clk: clk}
}

// IssueCommand validates the request and returns a server-minted acknowledgement.
//
// This is the first end-to-end milestone (#1151): the handler is intentionally
// thin — it validates input and acks. Enqueuing the command into the L4 device
// command queue (the HTTP devicecommand slice's path) is follow-up enrichment,
// tracked separately. A domain error is returned as an *errcode.Error; the gRPC
// interceptor chain maps it to a status code (the full errcode→codes table is
// PR-12, the Kratos GRPCStatus() model).
func (s *Server) IssueCommand(
	ctx context.Context,
	req *commandv1.IssueCommandRequest,
) (*commandv1.IssueCommandResponse, error) {
	if req.GetDeviceId() == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"device_id is required")
	}
	if req.GetCommandType() == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"command_type is required")
	}
	return &commandv1.IssueCommandResponse{
		AckId:                  uuid.NewString(),
		AcknowledgedAtUnixNano: s.clk.Now().UnixNano(),
	}, nil
}
