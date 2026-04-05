// Package postgres provides PostgreSQL adapter implementations for the GoCell
// framework. It includes connection pooling, transaction management, outbox
// pattern support, migration helpers, and error mapping.
//
// ref: jackc/pgx v5 — connection pool, transaction lifecycle
// Adopted: pgxpool.Pool wrapping, context-based tx propagation.
// Deviated: explicit TxFromContext helper instead of pgx BeginFunc pattern,
// to align with GoCell's outbox.Writer transactional boundary requirement.
package postgres
