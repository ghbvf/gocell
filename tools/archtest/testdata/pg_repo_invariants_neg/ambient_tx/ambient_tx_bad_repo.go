// Package postgres is a synthetic fixture that intentionally violates
// PG-REPO-AMBIENT-TX-01 by calling pool.Begin directly inside a method.
// This file is used only by TestPGRepoAmbientTx01_NegativeFixture.
package postgres

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// AmbientTxBadRepo holds a pool field named "pool" to trigger the scanner.
type AmbientTxBadRepo struct {
	pool *pgxpool.Pool
}

// Create is a CRUD method that violates PG-REPO-AMBIENT-TX-01 by calling
// pool.Begin directly instead of using txRunner.RunInTx or execCtx.
func (r *AmbientTxBadRepo) Create(ctx context.Context, id string) error {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	_, err = tx.Exec(ctx, "INSERT INTO bad_table (id) VALUES ($1)", id)
	return err
}
