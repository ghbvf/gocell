// Package postgres is a synthetic fixture that intentionally violates
// PG-REPO-ROLLBACK-REDACT-01 by having a function that calls tx.Rollback and
// slog.Error but does NOT call redaction.RedactError.
// This file is used only by TestPGRepoRollbackRedact01_NegativeFixture.
package postgres

import (
	"context"
	"log/slog"
)

// rollbackTx is a minimal interface for the fixture.
type rollbackTx interface {
	Rollback(ctx context.Context) error
}

// badRollbackHelper calls tx.Rollback and slog.Error but omits
// redaction.RedactError, violating PG-REPO-ROLLBACK-REDACT-01.
func badRollbackHelper(ctx context.Context, tx rollbackTx, err error) {
	if rbErr := tx.Rollback(ctx); rbErr != nil {
		// VIOLATION: this logs the rollback error without redacting it first.
		slog.Error("rollback failed", slog.String("error", rbErr.Error()))
	}
	_ = err
}
