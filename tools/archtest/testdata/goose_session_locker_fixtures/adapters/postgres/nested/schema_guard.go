// Nested adapters/postgres/nested/schema_guard.go shares the basename of the
// allowlisted file but lives at a different repo-relative path; the rel-path
// allowlist must NOT exempt it. WithSessionLocker missing → violation expected.
package nested

import "fixturetest/goose_session_locker/internal/goose"

func nestedSameBasenameViolates() error {
	_, err := goose.NewProvider(
		goose.WithTableName("schema_migrations"),
	)
	return err
}
