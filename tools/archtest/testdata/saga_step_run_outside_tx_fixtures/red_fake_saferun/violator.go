//go:build archtest_fixture

package redfakesaferun

import (
	"context"

	ksaga "github.com/ghbvf/gocell/kernel/saga"
)

// safeRun is a FAKE same-named helper that is NOT runtime/saga/executor.safeRun.
// A1's sanctioned range binds to the executor package by *types.Func identity,
// so the StepFunc call inside this impostor is still flagged (name match is not
// a sanction).
func safeRun(ctx context.Context, fn ksaga.StepFunc, inst *ksaga.Instance, prev []byte) ([]byte, error) {
	return fn(ctx, inst, prev)
}
