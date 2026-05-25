//go:build archtest_fixture

package redextrafile

import (
	"context"

	ksaga "github.com/ghbvf/gocell/kernel/saga"
)

// runStepDirectly calls a ksaga.StepFunc from a top-level function that is
// NOT named safeRun. SAGA-STEP-RUN-OUTSIDE-TX-01 A1 must flag this.
func runStepDirectly(ctx context.Context, fn ksaga.StepFunc, inst *ksaga.Instance, prev []byte) ([]byte, error) {
	// Direct StepFunc invocation outside safeRun — violates A1.
	return fn(ctx, inst, prev)
}
