// Dot import + WithSessionLocker MISSING → violation expected.
// The pre-typed scanner missed this entirely (no SelectorExpr to inspect).
package postgres

import . "fixturetest/goose_session_locker/internal/goose"

func dotViolates() error {
	_, err := NewProvider(
		WithTableName("schema_migrations"),
	)
	return err
}
