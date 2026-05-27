//go:build archtest_fixture

package redcompensatenamedfunc

import (
	"context"
	"database/sql"

	ksaga "github.com/ghbvf/gocell/kernel/saga"
)

// badCompensate is a named function with the CompensateFunc signature.
// Its body calls (*sql.Tx).Exec — a forbidden durable side effect.
func badCompensate(ctx context.Context, inst *ksaga.Instance, committedState []byte) error {
	var tx *sql.Tx
	// Violation: calls a method on *sql.Tx inside a CompensateFunc-typed function.
	_, err := tx.Exec("DELETE FROM saga_trace WHERE id = $1", inst.ID)
	return err
}

// buildStepWithNamedCompensate assigns the named function to a
// ksaga.CompensateFunc-typed variable via CompositeLit, triggering the
// named-function assignment detection path.
func buildStepWithNamedCompensate() ksaga.Step {
	return ksaga.Step{
		Compensate: badCompensate,
	}
}

// Ensure the named function is assignable to the typed slot.
var _ ksaga.CompensateFunc = badCompensate
