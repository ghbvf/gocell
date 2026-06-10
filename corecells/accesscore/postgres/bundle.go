// Package postgres exposes the typed funnel for PostgreSQL-backed accesscore
// wiring. A single NewBundle call derives the UserRepository, RoleRepository,
// SetupLock and TxRunner from the same (pool, txMgr, clock) triple — the
// Bundle struct is sealed (private fields, exported only via accessor methods
// consumed by corecells/accesscore.WithPGBundle) so composition roots cannot
// accidentally pair primitives from different pools or substitute a non-PG
// SetupLock.
//
// See corecells/accesscore/mem.Bundle godoc for the upstream-Hard / downstream-Hard
// analysis; this package follows the same pattern.
package postgres

import (
	"github.com/jackc/pgx/v5/pgxpool"

	accessrepo "github.com/ghbvf/gocell/corecells/accesscore/internal/adapters/postgres"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// Bundle is the PG-backed (UserRepository, RoleRepository, SetupLock,
// TxRunner) quadruple. Fields are unexported; corecells/accesscore.WithPGBundle
// consumes the values via the exported accessor methods.
type Bundle struct {
	userRepo  ports.UserRepository
	roleRepo  ports.RoleRepository
	setupLock ports.SetupLockAcquirer
	txRunner  persistence.CellTxManager
}

// NewBundle constructs a PG-backed accesscore bundle. All four wired
// primitives originate from the same (pool, txMgr, clk) triple — the
// pg_advisory_xact_lock acquired by SetupLock and the transactions run by
// TxRunner share the same database session.
//
// Fails fast on nil pool / typed-nil txMgr / typed-nil clk.
func NewBundle(pool *pgxpool.Pool, txMgr persistence.TxRunner, clk clock.Clock) (Bundle, error) {
	if pool == nil {
		return Bundle{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore/postgres.NewBundle: pool must not be nil")
	}
	if validation.IsNilInterface(txMgr) {
		return Bundle{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore/postgres.NewBundle: txMgr must not be nil")
	}
	if validation.IsNilInterface(clk) {
		return Bundle{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore/postgres.NewBundle: clock must not be nil")
	}
	userRepo, err := accessrepo.NewPGUserRepo(pool, txMgr, clk)
	if err != nil {
		return Bundle{}, err
	}
	roleRepo, err := accessrepo.NewPGRoleRepo(pool, txMgr, clk)
	if err != nil {
		return Bundle{}, err
	}
	setupLock, err := accessrepo.NewPGSetupLock(txMgr)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{
		userRepo:  userRepo,
		roleRepo:  roleRepo,
		setupLock: setupLock,
		txRunner:  persistence.WrapForCell(txMgr),
	}, nil
}

// UserRepository returns the bundle-paired PG UserRepository.
func (b Bundle) UserRepository() ports.UserRepository { return b.userRepo }

// RoleRepository returns the bundle-paired PG RoleRepository.
func (b Bundle) RoleRepository() ports.RoleRepository { return b.roleRepo }

// SetupLock returns the bundle-paired PG advisory lock.
func (b Bundle) SetupLock() ports.SetupLockAcquirer { return b.setupLock }

// TxRunner returns the bundle-paired CellTxManager wrapping the same txMgr.
func (b Bundle) TxRunner() persistence.CellTxManager { return b.txRunner }
