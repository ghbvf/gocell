// Package postgres is a synthetic fixture that intentionally violates
// PG-CONSTRUCTOR-MUST-FREE-01 by declaring an exported MustNew* constructor.
// This file is used only by TestPGConstructorMustFree01_NegativeFixture.
package postgres

import "github.com/jackc/pgx/v5/pgxpool"

// BadRepo is a fake repository for the negative fixture.
type BadRepo struct {
	pool *pgxpool.Pool
}

// MustNewBadRepo is the violating exported MustNew* constructor that
// PG-CONSTRUCTOR-MUST-FREE-01 is designed to catch. Real repos must use
// error-first NewXxx instead.
func MustNewBadRepo(pool *pgxpool.Pool) *BadRepo {
	if pool == nil {
		panic("pool must not be nil")
	}
	return &BadRepo{pool: pool}
}
