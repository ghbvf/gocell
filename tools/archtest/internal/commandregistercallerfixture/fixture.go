//go:build archtest_fixture

// Package commandregistercallerfixture is the RED fixture for
// COMMAND-DISPATCH-REGISTER-CALLER-01.
//
// It simulates a foreign cell package (outside generated/contracts/command/)
// that reaches (*command.Registry).RegisterHandler and
// (*command.Registry).LookupHandler through FOUR distinct syntactic shapes the
// invariant claims to cover. The scanner must detect every one and report ≥ 4
// diagnostics:
//
//  1. Direct call:        reg.RegisterHandler(...)
//  2. Direct call:        reg.LookupHandler(...)
//  3. Method value:       f := reg.RegisterHandler; f(...)
//  4. Method expression:  (*command.Registry).LookupHandler(reg, ...)
//
// Shapes 3 and 4 are the indirection forms the godoc of
// COMMAND-DISPATCH-REGISTER-CALLER-01 asserts are closed; locking them here
// prevents the scanner from silently regressing to call-only detection.
//
// DO NOT use this package in production code.
package commandregistercallerfixture

import (
	"context"

	"github.com/ghbvf/gocell/runtime/command"
)

// foreignHandler is a dummy handler type for wiring into the registry.
type foreignHandler struct{}

func (foreignHandler) Handle(_ context.Context) error { return nil }

// BadRegisterDirect calls RegisterHandler directly from a foreign (non-generated)
// package. This is the violation shape the COMMAND-DISPATCH-REGISTER-CALLER-01
// archtest must detect.
func BadRegisterDirect(reg *command.Registry) error {
	return reg.RegisterHandler("test.command.v1", foreignHandler{})
}

// BadLookupDirect calls LookupHandler directly from a foreign package. The
// invariant requires all LookupHandler calls to originate from generated code
// under generated/contracts/command/**.
func BadLookupDirect(reg *command.Registry) (any, bool) {
	return reg.LookupHandler("test.command.v1")
}

// BadRegisterMethodValue reaches RegisterHandler as a METHOD VALUE
// (f := reg.RegisterHandler) and invokes it indirectly. The reference lives at
// the `reg.RegisterHandler` selector — not the f(...) call — so the scanner must
// match the selector to close the "indirection through a method value" gap.
func BadRegisterMethodValue(reg *command.Registry) error {
	f := reg.RegisterHandler
	return f("test.command.v1", foreignHandler{})
}

// BadLookupMethodExpr reaches LookupHandler as a METHOD EXPRESSION
// ((*command.Registry).LookupHandler) and applies it with an explicit receiver.
// The reference lives at the `(*command.Registry).LookupHandler` selector; the
// scanner must match the method-expression form, not only method values.
func BadLookupMethodExpr(reg *command.Registry) (any, bool) {
	f := (*command.Registry).LookupHandler
	return f(reg, "test.command.v1")
}
