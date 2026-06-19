package devicecommand

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/command"
	cmdremote "github.com/ghbvf/gocell/generated/contracts/command/remotecommand/v1"
)

// RemoteCommandAdapter bridges the generated synchronous command-bus Handler
// for command.remotecommand.v1 to the domain Service.Enqueue method.
//
// It is the command-bus analog of EnqueueAdapter (which bridges the HTTP
// enqueue contract): both wrap the same devicecmd.Service so the HTTP entry
// point and the sync command-bus entry point share one business path. The
// composition root registers it via cmdremote.Register(reg, RemoteCommandAdapter{S})
// in the cell's Init — making the generated funnel a live import rather than
// dead-but-compiles (#1580).
//
// cmdremote.Register is the SOLE sanctioned registration path; calling
// command.Registry.RegisterHandler directly is rejected by archtest
// COMMAND-DISPATCH-REGISTER-CALLER-01.
//
// Naming convention within this slice: the HTTP entry-point bridge is
// <Op>Adapter (e.g. EnqueueAdapter), the command-bus entry-point bridge is
// <Op>CommandAdapter — the two suffixes distinguish the two entry points that
// share one domain Service.
type RemoteCommandAdapter struct{ S *Service }

// Compile-time proof the adapter satisfies the generated Handler interface.
var _ cmdremote.Handler = RemoteCommandAdapter{}

// HandleRemotecommand implements cmdremote.Handler. It delegates to the same
// Service.Enqueue the HTTP enqueue adapter uses, then maps the resulting
// command.Entry into the generated command response DTO.
func (a RemoteCommandAdapter) HandleRemotecommand(ctx context.Context, req *cmdremote.Request) (*cmdremote.Response, error) {
	entry, err := a.S.Enqueue(ctx, req.DeviceID, req.CommandType, req.Payload)
	if err != nil {
		return nil, err
	}
	return &cmdremote.Response{Data: toRemoteCommandResponseData(entry)}, nil
}

// toRemoteCommandResponseData maps a command.Entry into the generated command
// response DTO. It reuses entryToFields (handler.go), the canonical projection
// shared with every HTTP response converter in this slice.
func toRemoteCommandResponseData(e command.Entry) *cmdremote.ResponseData {
	f := entryToFields(e)
	return &cmdremote.ResponseData{
		ID: f.ID, DeviceID: f.DeviceID, CommandType: f.CommandType,
		Payload: f.Payload, Status: f.Status, Attempt: f.Attempt,
		CreatedAt: f.CreatedAt, SentAt: f.SentAt,
		DeliveredAt: f.DeliveredAt, CompletedAt: f.CompletedAt,
	}
}
