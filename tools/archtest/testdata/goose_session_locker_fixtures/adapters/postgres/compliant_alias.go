// Aliased import + WithSessionLocker present → no violation.
// Demonstrates that the typed scanner accepts aliases as long as the locker
// option is wired up.
package postgres

import g "fixturetest/goose_session_locker/internal/goose"

func compliantAlias() error {
	_, err := g.NewProvider(
		g.WithTableName("schema_migrations"),
		g.WithSessionLocker(g.NewLocker()),
	)
	return err
}
