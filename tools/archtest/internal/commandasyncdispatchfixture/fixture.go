//go:build archtest_fixture

// Package commandasyncdispatchfixture is the RED fixture for
// COMMAND-ASYNC-DISPATCH-CALLER-01.
//
// It binds NON-generated values into (*outbox.Relay).WithCommandDispatch — the
// shape the invariant forbids. The relay's command-dispatch map values MUST be
// generated DispatchAsync symbols from generated/contracts/command/**, never a
// hand-rolled func or a wrapper closure (which would bypass the typed Handler +
// registry single-handler guarantee). Two violation shapes are present so the
// self-check can lock both:
//
//  1. A package-level local func reference (notGenerated).
//  2. An inline func literal.
//
// DO NOT use this package in production code.
package commandasyncdispatchfixture

import (
	"context"

	kout "github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/command"
	"github.com/ghbvf/gocell/runtime/outbox"
)

// notGenerated is a hand-rolled command.AsyncDispatchFunc that bypasses the
// generated DispatchAsync funnel. Binding it into WithCommandDispatch is the
// violation COMMAND-ASYNC-DISPATCH-CALLER-01 must flag.
func notGenerated(_ context.Context, _ *command.Registry, _ kout.Entry) error {
	return nil
}

// BadWithCommandDispatch wires non-generated dispatch values into the relay,
// the exact bypass the archtest must detect (≥ 2 violations).
func BadWithCommandDispatch(r *outbox.Relay, reg *command.Registry) {
	r.WithCommandDispatch(reg, map[command.CommandID]command.AsyncDispatchFunc{
		"command.bad.local.v1":   notGenerated,
		"command.bad.closure.v1": func(_ context.Context, _ *command.Registry, _ kout.Entry) error { return nil },
	})
}
