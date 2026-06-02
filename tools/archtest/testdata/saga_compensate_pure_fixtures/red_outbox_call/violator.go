//go:build archtest_fixture

package redcompensateoutboxcall

import (
	"context"

	"github.com/ghbvf/gocell/kernel/outbox"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
)

// buildStepWithOutboxCompensate creates a saga.Step whose Compensate func
// literal calls outbox.Writer.Write — a forbidden durable side effect.
// SAGA-STEP-COMPENSATE-PURE-01 A1 must flag the Write call.
func buildStepWithOutboxCompensate(w outbox.Writer) ksaga.Step {
	return ksaga.Step{
		Compensate: func(ctx context.Context, inst *ksaga.Instance, committedState []byte) error {
			// Violation: CompensateFunc body calls a method on outbox.Writer.
			return w.Write(ctx, outbox.Entry{})
		},
	}
}
