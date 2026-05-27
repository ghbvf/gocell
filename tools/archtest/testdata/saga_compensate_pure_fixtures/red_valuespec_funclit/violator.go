//go:build archtest_fixture

package redcompensatevaluespecfunclit

import (
	"context"

	"github.com/ghbvf/gocell/kernel/outbox"
	ksaga "github.com/ghbvf/gocell/kernel/saga"
)

// buildValueSpecCompensate declares a ksaga.CompensateFunc-typed variable via a
// ValueSpec whose func-literal initializer calls outbox.Writer.Write — a
// forbidden durable side effect. SAGA-STEP-COMPENSATE-PURE-01 A1 must flag the
// Write call (form 1: ValueSpec + func literal).
func buildValueSpecCompensate(w outbox.Writer) ksaga.CompensateFunc {
	var c ksaga.CompensateFunc = func(ctx context.Context, inst *ksaga.Instance, committedState []byte) error {
		// Violation: CompensateFunc body calls a method on outbox.Writer.
		return w.Write(ctx, outbox.Entry{})
	}
	return c
}
