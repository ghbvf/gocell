// Package postgres exercises the goose_session_locker archtest with a
// vanilla import. WithSessionLocker is present → no violation.
package postgres

import "fixturetest/goose_session_locker/internal/goose"

func compliantDefault() error {
	_, err := goose.NewProvider(
		goose.WithTableName("schema_migrations"),
		goose.WithSessionLocker(goose.NewLocker()),
	)
	return err
}
