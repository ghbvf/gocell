package accesscore

import (
	"github.com/jackc/pgx/v5/pgxpool"

	accessrepo "github.com/ghbvf/gocell/cells/accesscore/internal/adapters/postgres"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

// PGDeps bundles the three PostgreSQL dependencies required by the accesscore
// PG-backed repositories. Callers in cmd/* construct this once via NewPGDeps
// and pass it to NewPGUserRepository / NewPGRoleRepository / NewPGSetupLock.
//
// The unexported pool field intentionally hides *pgxpool.Pool from the cell's
// exported API surface (LAYER-10). NewPGDeps accepts pool as any so that the
// exported function signature does not reference the pgxpool package directly —
// the type assertion inside NewPGDeps is the only reference at this boundary.
type PGDeps struct {
	pool     *pgxpool.Pool
	txRunner persistence.TxRunner
	clock    clock.Clock
}

// NewPGDeps constructs a PGDeps bundle. Fails fast when any dependency is nil
// or when pool is not a *pgxpool.Pool (programming error — startup time panic).
//
// pool is typed as any so that the exported function signature does not expose
// pgxpool in the cells/accesscore public API (LAYER-10). Callers in cmd/* pass
// the value returned by adapterpg.Pool.DB() which is *pgxpool.Pool; passing any
// other type results in an immediate ErrValidationFailed at construction time.
func NewPGDeps(pool any, tx persistence.TxRunner, clk clock.Clock) (PGDeps, error) {
	if pool == nil {
		return PGDeps{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore.NewPGDeps: pool must not be nil")
	}
	p, ok := pool.(*pgxpool.Pool)
	if !ok {
		return PGDeps{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore.NewPGDeps: pool must be *pgxpool.Pool")
	}
	if validation.IsNilInterface(tx) {
		return PGDeps{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore.NewPGDeps: txRunner must not be nil")
	}
	if validation.IsNilInterface(clk) {
		return PGDeps{}, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"accesscore.NewPGDeps: clock must not be nil")
	}
	return PGDeps{pool: p, txRunner: tx, clock: clk}, nil
}

// NewPGUserRepository constructs the PG-backed cell-private
// ports.UserRepository implementation. The returned value is wired into the
// cell via WithUserRepository; callers in cmd/* never name the underlying
// concrete type because cells/accesscore/internal/adapters/postgres is
// inaccessible across the internal package boundary.
//
// Composition-root convenience — the actual implementation lives in
// cells/accesscore/internal/adapters/postgres/user_repo.go (S3+S5).
func NewPGUserRepository(deps PGDeps) (ports.UserRepository, error) {
	return accessrepo.NewPGUserRepo(deps.pool, deps.txRunner, deps.clock)
}

// NewPGRoleRepository constructs the PG-backed cell-private
// ports.RoleRepository implementation. Same boundary-bridging rationale as
// NewPGUserRepository — see godoc above.
func NewPGRoleRepository(deps PGDeps) (ports.RoleRepository, error) {
	return accessrepo.NewPGRoleRepo(deps.pool, deps.txRunner, deps.clock)
}

// NewPGSetupLock constructs a PG advisory lock for the admin-provisioning
// cross-process serialization. Returns a ports.SetupLock interface so the
// cell-private *PGSetupLock type does not escape to cmd/*.
//
// The advisory lock uses pg_advisory_xact_lock so it is automatically released
// at transaction commit or rollback — no explicit Release call is needed.
// Callers must invoke Acquire inside an open transaction context (i.e. inside
// txRunner.RunInTx). Closes backlog ADMINPROVISION-DIST-LOCK-01.
func NewPGSetupLock(deps PGDeps) (ports.SetupLock, error) {
	return accessrepo.NewPGSetupLock(deps.txRunner)
}
