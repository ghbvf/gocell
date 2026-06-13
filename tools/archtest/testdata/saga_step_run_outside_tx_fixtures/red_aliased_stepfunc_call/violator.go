//go:build archtest_fixture

package redaliasedstepfunccall

import (
	"context"

	renamedsaga "github.com/ghbvf/gocell/kernel/saga"
)

// AliasStep is a type ALIAS of StepFunc (same *types.Named — old A1 caught
// this one; signature A1 still does).
type AliasStep = renamedsaga.StepFunc

// DefStep is a DEFINED type over StepFunc (distinct *types.Named — old
// exact-Named A1 MISSED calls through it; signature A1 catches it because the
// underlying signature is identical).
type DefStep renamedsaga.StepFunc

// runViaAlias calls an alias-typed StepFunc value outside safeRun.
func runViaAlias(ctx context.Context, fn AliasStep, inst *renamedsaga.Instance, prev []byte) ([]byte, error) {
	return fn(ctx, inst, prev)
}

// runViaDefinedType calls a defined-type StepFunc value outside safeRun —
// old A1 missed (distinct Named); signature A1 flags it.
func runViaDefinedType(ctx context.Context, fn DefStep, inst *renamedsaga.Instance, prev []byte) ([]byte, error) {
	return fn(ctx, inst, prev)
}

// runViaRawSignature calls a structurally-identical raw func value outside
// safeRun — no named type at all; old A1 missed (no *types.Named); signature
// A1 flags it.
func runViaRawSignature(ctx context.Context, fn func(context.Context, *renamedsaga.Instance, []byte) ([]byte, error), inst *renamedsaga.Instance, prev []byte) ([]byte, error) {
	return fn(ctx, inst, prev)
}
