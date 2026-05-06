// Aliased import + WithSessionLocker MISSING → violation expected.
// The pre-typed scanner missed this entirely (sel.X.Name == "g" ≠ "goose").
package postgres

import g "fixturetest/goose_session_locker/internal/goose"

func aliasedViolates() error {
	_, err := g.NewProvider(
		g.WithTableName("schema_migrations"),
	)
	return err
}
