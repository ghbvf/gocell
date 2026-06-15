//go:build archtest_fixture

// Package commandasyncemitcallerfixture is the RED fixture for
// COMMAND-ASYNC-EMIT-CALLER-01.
//
// It calls the runtime async-command emit exits — command.EmitAsync and
// command.EmitAsyncFromIdempotencyKey — DIRECTLY with a bare DispatchID,
// bypassing the generated per-command EmitAsync wrapper that bakes in DispatchID
// and locks the payload to the typed *Request (#2059). Both are violations the
// scanner must flag: leaving either exit open re-admits the bare-DispatchID
// dispatch the wrapper funnel exists to eliminate.
//
// DO NOT use this package in production code.
package commandasyncemitcallerfixture

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	kout "github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/runtime/command"
)

// dispatchID is a bare command DispatchID a cell should never name directly —
// the generated wrapper bakes it in.
const dispatchID command.CommandID = "command.devicecommand.enqueue.v1"

type payload struct {
	Foo string `json:"foo"`
}

// BadEmitAsync calls runtime command.EmitAsync directly with a bare DispatchID,
// bypassing the generated per-command EmitAsync wrapper — the primary bypass.
func BadEmitAsync(ctx context.Context, emitter kout.Emitter) error {
	return command.EmitAsync(ctx, clock.Real(), emitter, dispatchID,
		"subject", "cmd-1", payload{Foo: "bar"})
}

// BadEmitAsyncFromIdempotencyKey calls the HTTP-bridge exit directly with a bare
// DispatchID — the second bypass the funnel must close.
func BadEmitAsyncFromIdempotencyKey(ctx context.Context, emitter kout.Emitter) error {
	return command.EmitAsyncFromIdempotencyKey(ctx, clock.Real(), emitter, dispatchID,
		"subject", payload{Foo: "bar"})
}
