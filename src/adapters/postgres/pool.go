// Package postgres provides PostgreSQL adapters for the GoCell framework.
//
// ref: ThreeDotsLabs/watermill-sql pkg/sql/publisher.go — ContextExecutor abstraction
// Adopted: thin DB wrapper with interface-based query execution.
// Deviated: Pool wraps *sql.DB directly rather than accepting both DB and Tx,
// because GoCell uses context-embedded transactions via TxManager.
package postgres

import (
	"context"
	"database/sql"
)

// Pool wraps a *sql.DB and provides convenience methods for transaction
// management and query execution.
type Pool struct {
	db *sql.DB
}

// NewPool creates a new Pool wrapping the given *sql.DB.
func NewPool(db *sql.DB) *Pool {
	return &Pool{db: db}
}

// DB returns the underlying *sql.DB.
func (p *Pool) DB() *sql.DB {
	return p.db
}

// BeginTx starts a new transaction with the given options.
func (p *Pool) BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error) {
	return p.db.BeginTx(ctx, opts)
}

// QueryContext executes a query that returns rows.
func (p *Pool) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	return p.db.QueryContext(ctx, query, args...)
}

// ExecContext executes a statement that does not return rows.
func (p *Pool) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	return p.db.ExecContext(ctx, query, args...)
}

// Close closes the underlying database connection pool.
func (p *Pool) Close() error {
	return p.db.Close()
}
