//go:build archtest_fixture

// Package commandregistercallerfixture is the RED fixture for
// COMMAND-DISPATCH-REGISTER-CALLER-01.
//
// It simulates a foreign cell package (outside generated/contracts/command/)
// that directly calls (*command.Registry).RegisterHandler and
// (*command.Registry).LookupHandler. These calls are forbidden by the
// invariant; the archtest scanner must detect both and report ≥ 2 diagnostics.
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
