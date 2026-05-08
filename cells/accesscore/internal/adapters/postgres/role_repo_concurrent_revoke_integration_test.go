//go:build integration

package postgres

import (
	"context"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// concurrentRevokeBundle extends repoBundle with the adapterpg.Pool needed to
// construct a TxManager for the concurrent-revoke integration test.
type concurrentRevokeBundle struct {
	repoBundle
	adapPool *adapterpg.Pool
}

func setupConcurrentRevokeBundle(t *testing.T) concurrentRevokeBundle {
	t.Helper()
	p := setupPGPool(t)
	roleRepo, err := NewPGRoleRepository(p.DB(), clock.Real())
	require.NoError(t, err)
	userRepo, err := NewPGUserRepository(p.DB())
	require.NoError(t, err)
	return concurrentRevokeBundle{
		repoBundle: repoBundle{roleRepo: roleRepo, userRepo: userRepo, pool: p.DB()},
		adapPool:   p,
	}
}

// TestRemoveFromUserIfNotLast_ConcurrentRevoke verifies that the advisory lock
// inside removeIfNotLastWithLockSQL correctly serializes 8 concurrent revokes
// for the same role: 7 succeed (changed=true) and exactly 1 is rejected with
// ErrAuthForbidden because it is the sole remaining holder at that point.
//
// Uses role "guest" (not "admin") to avoid the single-admin partial-index
// side effects.
func TestRemoveFromUserIfNotLast_ConcurrentRevoke(t *testing.T) {
	b := setupConcurrentRevokeBundle(t)
	ctx := context.Background()

	// Create a "guest" role.
	guestRole := &domain.Role{
		ID:          "guest-" + uuid.NewString()[:8],
		Name:        "guest-concurrent-" + uuid.NewString()[:8],
		Permissions: []domain.Permission{},
	}
	require.NoError(t, b.roleRepo.Create(ctx, guestRole))

	const N = 8
	users := make([]string, N)
	for i := range N {
		users[i] = createTestUser(t, ctx, b.userRepo)
		_, err := b.roleRepo.AssignToUser(ctx, users[i], guestRole.ID)
		require.NoError(t, err)
	}

	// Verify initial count.
	count, err := b.roleRepo.CountByRole(ctx, guestRole.ID)
	require.NoError(t, err)
	require.Equal(t, N, count, "all 8 users must hold the role before concurrent revoke")

	type result struct {
		changed bool
		err     error
	}
	resultsCh := make(chan result, N)

	// TxManager for ambient TX.
	txm := adapterpg.NewTxManager(b.adapPool)

	var wg sync.WaitGroup
	wg.Add(N)
	for i := range N {
		go func(idx int) {
			defer wg.Done()
			var (
				changed bool
				rErr    error
			)
			txErr := txm.RunInTx(ctx, func(txCtx context.Context) error {
				changed, rErr = b.roleRepo.RemoveFromUserIfNotLast(txCtx, users[idx], guestRole.ID)
				return rErr
			})
			if txErr != nil {
				resultsCh <- result{changed: false, err: txErr}
				return
			}
			resultsCh <- result{changed: changed, err: nil}
		}(i)
	}
	wg.Wait()
	close(resultsCh)

	changedCount := 0
	forbiddenCount := 0
	for r := range resultsCh {
		if r.err == nil && r.changed {
			changedCount++
			continue
		}
		if r.err != nil {
			var ec *errcode.Error
			require.ErrorAs(t, r.err, &ec, "unexpected error type: %v", r.err)
			assert.Equal(t, errcode.ErrAuthForbidden, ec.Code,
				"sole holder must return ErrAuthForbidden")
			forbiddenCount++
			continue
		}
		// changed=false, err=nil should not happen here (all users hold the role).
		t.Errorf("unexpected changed=false, err=nil for concurrent revoke")
	}

	assert.Equal(t, N-1, changedCount, "N-1 revokes must succeed")
	assert.Equal(t, 1, forbiddenCount, "exactly 1 revoke must be rejected as sole holder")

	// Final row count must be 1 (the sole holder that could not be revoked).
	finalCount, err := b.roleRepo.CountByRole(ctx, guestRole.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, finalCount, "exactly 1 holder must remain after concurrent revoke")
}
