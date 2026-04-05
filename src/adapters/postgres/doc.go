// Package postgres provides a PostgreSQL adapter for the GoCell framework.
//
// It implements the database interfaces defined in kernel/ and runtime/,
// providing connection pooling, transaction management, schema migrations,
// and the outbox pattern for reliable event publishing.
//
// # Configuration
//
// The adapter accepts a DSN (Data Source Name) string or structured config:
//
//	DSN:             "postgres://user:pass@host:5432/dbname?sslmode=disable"
//	MaxOpenConns:    25
//	MaxIdleConns:    5
//	ConnMaxLifetime: 5m
//
// # Outbox Pattern
//
// The outbox table stores pending domain events alongside business data in
// the same transaction, guaranteeing at-least-once delivery. A background
// poller publishes pending entries to the message broker and marks them as
// delivered.
//
// # Close
//
// Always call Close to release the connection pool on shutdown.
package postgres
