//go:build archtest_fixture

package redcompensateassignfunclit

import (
	"context"
	"database/sql"

	ksaga "github.com/ghbvf/gocell/kernel/saga"
)

// buildAssignCompensate assigns a func literal to a previously-declared
// ksaga.CompensateFunc-typed variable via an AssignStmt. The literal body calls
// (*sql.Tx).Exec — a forbidden durable side effect. SAGA-STEP-COMPENSATE-PURE-01
// A1 must flag the Exec call (form 2: AssignStmt + func literal).
func buildAssignCompensate(tx *sql.Tx) ksaga.CompensateFunc {
	var c ksaga.CompensateFunc
	c = func(ctx context.Context, inst *ksaga.Instance, committedState []byte) error {
		// Violation: CompensateFunc body calls a method on *sql.Tx.
		_, err := tx.ExecContext(ctx, "DELETE FROM saga_trace WHERE id = $1", inst.ID)
		return err
	}
	return c
}
