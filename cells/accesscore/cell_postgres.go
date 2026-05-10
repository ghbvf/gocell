package accesscore

import (
	"github.com/jackc/pgx/v5/pgxpool"

	accessrepo "github.com/ghbvf/gocell/cells/accesscore/internal/adapters/postgres"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
)

// NewPGUserRepository constructs the PG-backed cell-private
// ports.UserRepository implementation. The returned value is wired into the
// cell via WithUserRepository; callers in cmd/* never name the underlying
// concrete type because cells/accesscore/internal/adapters/postgres is
// inaccessible across the internal package boundary.
//
// Composition-root convenience — the actual implementation lives in
// cells/accesscore/internal/adapters/postgres/user_repo.go (S3+S5).
//
// Returns errcode.New(KindInvalid, ErrValidationFailed, ...) on nil
// dependencies; the underlying constructor enforces typed-nil rejection.
func NewPGUserRepository(pool *pgxpool.Pool, txRunner persistence.TxRunner, clk clock.Clock) (ports.UserRepository, error) {
	return accessrepo.NewPGUserRepo(pool, txRunner, clk)
}

// NewPGRoleRepository constructs the PG-backed cell-private
// ports.RoleRepository implementation. Same boundary-bridging rationale as
// NewPGUserRepository — see godoc above.
func NewPGRoleRepository(pool *pgxpool.Pool, txRunner persistence.TxRunner, clk clock.Clock) (ports.RoleRepository, error) {
	return accessrepo.NewPGRoleRepo(pool, txRunner, clk)
}
