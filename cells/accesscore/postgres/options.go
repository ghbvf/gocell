// Package postgres wires PostgreSQL-backed repositories for accesscore.
//
// This package is intentionally outside cells/accesscore's root package so the
// Cell's exported API stays port-oriented while composition roots can still
// choose the concrete storage adapter. It mirrors the pattern established by
// cells/configcore/postgres/options.go.
//
// ref: cells/configcore/postgres/options.go (bridge-package pattern)
// ref: cells/accesscore/internal/adapters/postgres/user_repo.go (Dev A)
package postgres

import (
	"github.com/jackc/pgx/v5/pgxpool"

	accesscore "github.com/ghbvf/gocell/cells/accesscore"
	pgcell "github.com/ghbvf/gocell/cells/accesscore/internal/adapters/postgres"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// WithPool injects PostgreSQL-backed repositories into accesscore using the
// provided pool.
//
// clk must be non-nil; pass clock.Real() at the composition root. It is used
// by PGRoleRepository (which writes created_at/assigned_at timestamps).
//
// The caller is responsible for the pool lifecycle. This option constructs
// PGUserRepository, PGSessionRepository, and PGRoleRepository and injects
// them via the corresponding accesscore.With* options.
//
// Returns ErrCellInvalidConfig when pool or clk are nil so wiring mistakes
// fail before AccessCore is constructed with unusable repositories.
func WithPool(pool *pgxpool.Pool, clk clock.Clock) ([]accesscore.Option, error) {
	if pool == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore/postgres: WithPool requires a non-nil *pgxpool.Pool")
	}
	if validation.IsNilInterface(clk) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore/postgres: WithPool requires a non-nil clock.Clock")
	}

	userRepo, err := pgcell.NewPGUserRepository(pool)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore/postgres: NewPGUserRepository", err)
	}
	sessionRepo, err := pgcell.NewPGSessionRepository(pool)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore/postgres: NewPGSessionRepository", err)
	}
	roleRepo, err := pgcell.NewPGRoleRepository(pool, clk)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore/postgres: NewPGRoleRepository", err)
	}
	return []accesscore.Option{
		accesscore.WithUserRepository(userRepo),
		accesscore.WithSessionRepository(sessionRepo),
		accesscore.WithRoleRepository(roleRepo),
	}, nil
}
