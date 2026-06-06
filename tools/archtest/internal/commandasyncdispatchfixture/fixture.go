//go:build archtest_fixture

// Package commandasyncdispatchfixture is the RED fixture for
// COMMAND-ASYNC-DISPATCH-CALLER-01.
//
// It exercises the violation shapes COMMAND-ASYNC-DISPATCH-CALLER-01 forbids so
// the scanner's RED self-check can lock each one:
//
//	(map shape, #1667 + #1673 F1) WithCommandDispatch bound a dispatch map whose
//	    VALUE is a hand-rolled func / inline closure (not a generated DispatchAsync)
//	    and whose KEY is a free string (not a generated DispatchID const).
//	(form shape, #1673 F2) WithCommandDispatch referenced as a method-value capture
//	    (`f := r.WithCommandDispatch; f(...)`) and as a method expression
//	    (`(*outbox.Relay).WithCommandDispatch(r, ...)`) — both bypass or shift the
//	    static dispatch-map check, so they are rejected outright.
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

// BadWithCommandDispatch wires non-generated dispatch values under free-string
// keys into the relay — the map-shape bypass the archtest must detect.
func BadWithCommandDispatch(r *outbox.Relay, reg *command.Registry) {
	r.WithCommandDispatch(reg, map[command.CommandID]command.AsyncDispatchFunc{
		"command.bad.local.v1":   notGenerated,
		"command.bad.closure.v1": func(_ context.Context, _ *command.Registry, _ kout.Entry) error { return nil },
	})
}

// BadMethodValueCapture captures WithCommandDispatch as a method VALUE and calls
// it indirectly — the #1673 F2 bypass where the indirect call's Fun is an Ident,
// not a selector, so a call-only scan never inspects the dispatch map.
func BadMethodValueCapture(r *outbox.Relay, reg *command.Registry) {
	bind := r.WithCommandDispatch
	bind(reg, map[command.CommandID]command.AsyncDispatchFunc{
		"command.bad.captured.v1": notGenerated,
	})
}

// BadMethodExpression invokes WithCommandDispatch as a method EXPRESSION, which
// shifts the receiver into Args[0] so the dispatch map is no longer at Args[1] —
// the #1673 F2 form a fixed-arg-slot scan must reject outright.
func BadMethodExpression(r *outbox.Relay, reg *command.Registry) {
	(*outbox.Relay).WithCommandDispatch(r, reg, map[command.CommandID]command.AsyncDispatchFunc{
		"command.bad.methodexpr.v1": notGenerated,
	})
}
