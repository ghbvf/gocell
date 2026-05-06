// Top-level adapters/postgres/schema_guard.go is allowlisted by repo-relative
// path. WithSessionLocker is intentionally absent (read-only path).
package postgres

import "fixturetest/goose_session_locker/internal/goose"

func readOnlyAllowlisted() error {
	_, err := goose.NewProvider(
		goose.WithTableName("schema_migrations"),
	)
	return err
}
