// Package postgres exposes accesscore-owned PostgreSQL repository factories to
// composition roots while keeping the concrete implementations inside the
// cell's internal adapter tree.
package postgres

import (
	"github.com/jackc/pgx/v5/pgxpool"

	accessrepo "github.com/ghbvf/gocell/cells/accesscore/internal/adapters/postgres"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// Deps bundles the PostgreSQL dependencies required by the accesscore
// PG-backed repositories. Callers in cmd/* construct this once via NewDeps and
// pass it to NewUserRepository / NewRoleRepository / NewSetupLock.
//
// The unexported pool field intentionally hides *pgxpool.Pool from the root
// accesscore API surface. NewDeps accepts pool as any so composition roots can
// pass adapterpg.Pool.DB() without exporting pgx types from cells/accesscore.
type Deps struct {
	pool     *pgxpool.Pool
	txRunner persistence.TxRunner
	clock    clock.Clock
}

// NewDeps constructs a Deps bundle. Fails fast when any dependency is nil or
// when pool is not a *pgxpool.Pool.
func NewDeps(pool any, tx persistence.TxRunner, clk clock.Clock) (Deps, error) {
	if pool == nil {
		return Deps{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore/postgres.NewDeps: pool must not be nil")
	}
	p, ok := pool.(*pgxpool.Pool)
	if !ok {
		return Deps{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore/postgres.NewDeps: pool must be *pgxpool.Pool")
	}
	if validation.IsNilInterface(tx) {
		return Deps{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore/postgres.NewDeps: txRunner must not be nil")
	}
	if validation.IsNilInterface(clk) {
		return Deps{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore/postgres.NewDeps: clock must not be nil")
	}
	return Deps{pool: p, txRunner: tx, clock: clk}, nil
}

// NewUserRepository constructs the PG-backed cell-private UserRepository.
func NewUserRepository(deps Deps) (ports.UserRepository, error) {
	return accessrepo.NewPGUserRepo(deps.pool, deps.txRunner, deps.clock)
}

// NewRoleRepository constructs the PG-backed cell-private RoleRepository.
func NewRoleRepository(deps Deps) (ports.RoleRepository, error) {
	return accessrepo.NewPGRoleRepo(deps.pool, deps.txRunner, deps.clock)
}

// NewSetupLock constructs the PG advisory lock used by admin provisioning.
func NewSetupLock(deps Deps) (ports.SetupLock, error) {
	return accessrepo.NewPGSetupLock(deps.txRunner)
}
