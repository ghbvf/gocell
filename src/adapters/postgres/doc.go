// Package postgres provides a PostgreSQL adapter for GoCell.
//
// This adapter implements repository interfaces defined in kernel/ and cells/
// using the standard database/sql package with PostgreSQL. It is the primary
// relational storage adapter for Phase 3 production deployments.
//
// Configuration is done via PostgresConfig, which can be populated from
// environment variables using ConfigFromEnv().
//
// # Usage
//
//	cfg := postgres.ConfigFromEnv()
//	pool, err := postgres.New(ctx, cfg)
//	if err != nil { ... }
//	defer pool.Close()
//
// # Environment Variables
//
// See docs/guides/adapter-config-reference.md for the full variable listing.
// Key variables: POSTGRES_HOST, POSTGRES_PORT, POSTGRES_USER, POSTGRES_PASSWORD,
// POSTGRES_DB, POSTGRES_SSLMODE, POSTGRES_MAX_OPEN_CONNS, POSTGRES_MAX_IDLE_CONNS.
//
// # Error Codes
//
// All errors use the ERR_ADAPTER_POSTGRES_* code family from pkg/errcode.
package postgres
