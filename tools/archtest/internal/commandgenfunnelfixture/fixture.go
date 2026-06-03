//go:build archtest_fixture

// Package commandgenfunnelfixture is the RED fixture for
// COMMAND-GEN-FUNNEL-SOLE-EMITTER-01.
//
// It declares a hand-written look-alike trio that mirrors what contractgen
// emits into generated/contracts/command/**:
//   - Handler: a typed business interface with a Handle*-returning-(*, error) method
//   - Register: a free func that accepts *command.Registry
//   - Dispatch: a free func that accepts *command.Registry
//
// These declarations are forbidden outside generated/contracts/command/**
// (by cells/**, examples/**). The archtest scanner targeting declarations
// must detect all three and report ≥ 1 diagnostic per declaration category.
//
// NOTE: this fixture is scanned for DECLARATIONS, not CALLS. A cell legitimately
// calling generated Register(reg, h) is fine; it is the HAND-WRITTEN DECLARATION
// of an equivalent shape that is forbidden.
//
// DO NOT use this package in production code.
package commandgenfunnelfixture

import (
	"context"

	"github.com/ghbvf/gocell/runtime/command"
)

// BadRequest is a dummy request type for the look-alike handler.
type BadRequest struct {
	Payload string
}

// BadResponse is a dummy response type for the look-alike handler.
type BadResponse struct {
	Result string
}

// Handler is a hand-written look-alike of the generated Handler interface.
// The archtest COMMAND-GEN-FUNNEL-SOLE-EMITTER-01 must detect this declaration
// because it mirrors the generated shape (interface with Handle*-returning-(*BadResponse, error))
// but lives outside generated/contracts/command/**.
type Handler interface {
	HandleBadCommand(ctx context.Context, req *BadRequest) (*BadResponse, error)
}

// Register is a hand-written look-alike of the generated Register function.
// It accepts *command.Registry and a Handler — exactly the generated shape.
// The archtest must detect this as a forbidden declaration.
func Register(reg *command.Registry, h Handler) error {
	_ = reg
	_ = h
	return nil
}

// Dispatch is a hand-written look-alike of the generated Dispatch function.
// It accepts *command.Registry — exactly the generated shape.
// The archtest must detect this as a forbidden declaration.
func Dispatch(ctx context.Context, reg *command.Registry, req *BadRequest) (*BadResponse, error) {
	_ = ctx
	_ = reg
	_ = req
	return nil, nil
}
