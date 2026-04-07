// Package postgres provides a PostgreSQL adapter for the GoCell framework.
//
// It wraps pgx/v5 to offer:
//   - Pool: connection pool with DSN/env-based configuration and Health() probe.
//   - TxManager: RunInTx with context-embedded pgx.Tx, savepoint nesting, and
//     automatic panic rollback.
//   - Migrator: embed.FS-driven SQL migrations with up/down/status and a
//     schema_migrations tracking table.
//   - RowScanner helper for reducing boilerplate in repository implementations.
//
// For parameterized SQL query construction, see pkg/query.Builder.
//
// Error codes use the ERR_ADAPTER_PG_* prefix (see errcode.go in this package).
//
// ref: Watermill watermill-sql schema_adapter_postgresql.go — adopted advisory
// locking for migration init; diverged by using embed.FS instead of inline SQL.
package postgres
