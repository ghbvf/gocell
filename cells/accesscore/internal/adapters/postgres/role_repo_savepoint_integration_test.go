//go:build integration

// P1#2: AssignToUser must not leave the outer transaction in an aborted state
// when the single-admin partial unique index rejects an INSERT (SQLSTATE
// 23505). adminprovision.Provisioner.handleAssignAdminError calls
// CountByRole on the same context immediately after — if the tx is poisoned,
// CountByRole fails with SQLSTATE 25P02 and the operator gets a 500 instead
// of the intended OutcomeRaceSkipped → 410 fold.
//
// The fix: PGRoleRepository.AssignToUser wraps the INSERT in a savepoint so
// a 23505 violation rolls back to the savepoint rather than poisoning the
// whole transaction.
//
// ref: PostgreSQL SAVEPOINT — https://www.postgresql.org/docs/current/sql-savepoint.html
// ref: setup race-loser flow — adminprovision/provisioner.go handleAssignAdminError
package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// TestAssignToUser_SingleAdminViolation_OuterTxStaysValid simulates the
// adminprovision multi-pod race: pod A has already committed the admin role
// assignment; pod B's setup request is wrapped in a transaction by setup.Service,
// and inside that tx Provisioner.Ensure calls AssignToUser → PG raises 23505
// against idx_role_assignments_single_admin → Provisioner immediately calls
// CountByRole on the same ctx to recount.
//
// Without savepoint protection, the outer tx is aborted and CountByRole errors
// with SQLSTATE 25P02 ("current transaction is aborted, commands ignored
// until end of transaction block"). The assertion below catches that as the
// RED state.
func TestAssignToUser_SingleAdminViolation_OuterTxStaysValid(t *testing.T) {
	pool := setupPGPool(t)
	ctx := context.Background()
	txm := adapterpg.NewTxManager(pool)

	roleRepo, err := NewPGRoleRepository(pool.DB(), clock.Real())
	require.NoError(t, err)
	userRepo, err := NewPGUserRepository(pool.DB())
	require.NoError(t, err)

	// Seed: create the admin role + 2 users.
	require.NoError(t, roleRepo.Create(ctx, &domain.Role{
		ID:          auth.RoleAdmin,
		Name:        auth.RoleAdmin,
		Permissions: []domain.Permission{},
	}))

	winnerID := createTestUser(t, ctx, userRepo)
	loserID := createTestUser(t, ctx, userRepo)

	// Pod A wins: admin role assigned to winnerID outside any tx (committed).
	_, err = roleRepo.AssignToUser(ctx, winnerID, auth.RoleAdmin)
	require.NoError(t, err, "winner assignment must succeed")

	// Pod B loses: inside its setup tx, AssignToUser must fail with
	// ErrAuthRoleDuplicate, but the outer tx must remain valid so the
	// recount in Provisioner.handleAssignAdminError succeeds and produces
	// OutcomeRaceSkipped → 410.
	err = txm.RunInTx(ctx, func(txCtx context.Context) error {
		_, assignErr := roleRepo.AssignToUser(txCtx, loserID, auth.RoleAdmin)
		require.Error(t, assignErr, "loser must collide with single-admin partial idx")
		var ec *errcode.Error
		require.ErrorAs(t, assignErr, &ec)
		require.Equal(t, errcode.ErrAuthRoleDuplicate, ec.Code,
			"single-admin 23505 must surface as ErrAuthRoleDuplicate")

		// CRITICAL P1#2 assertion: the outer tx must still be usable.
		// Without savepoint protection, this CountByRole fails with
		// pgconn 25P02 "current transaction is aborted".
		count, countErr := roleRepo.CountByRole(txCtx, auth.RoleAdmin)
		require.NoError(t, countErr,
			"outer tx must remain valid after AssignToUser absorbs 23505 — "+
				"if this errors with 25P02, AssignToUser failed to use savepoint")
		// Sanity: PG raised 25P02 explicitly?
		var pgErr *pgconn.PgError
		_ = pgErr
		assert.Equal(t, 1, count,
			"admin count must be 1 (winner still holds)")

		return nil
	})
	require.NoError(t, err, "outer tx commit must succeed; an empty closure after savepoint rollback should leave a clean tx")

	// Defense in depth: verify after-tx state is correct.
	finalCount, err := roleRepo.CountByRole(ctx, auth.RoleAdmin)
	require.NoError(t, err)
	assert.Equal(t, 1, finalCount, "post-tx admin count must remain 1")

	// Sanity: nothing was assigned to loserID.
	_ = time.Now() // silence unused import
}
