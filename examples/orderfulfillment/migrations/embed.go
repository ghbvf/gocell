// Package migrations embeds the orderfulfillment example SQL migrations and
// declares the migration namespace used to run them under a separate goose
// tracking table (schema_migrations_orderfulfillment) so they never collide
// with the platform namespace (schema_migrations_platform).
//
// Both run.go (runtime self-migration) and the integration test (NewMigrator)
// must use FS and Namespace from this package to guarantee identical migration
// application semantics.
package migrations

import (
	"embed"

	"github.com/ghbvf/gocell/framework/pkg/migration"
)

// FS embeds all *.sql migration files in this directory.
//
//go:embed *.sql
var FS embed.FS

// Namespace is the migration tracking namespace for the orderfulfillment
// example. It maps to the goose tracking table schema_migrations_orderfulfillment.
const Namespace migration.Namespace = "orderfulfillment"
