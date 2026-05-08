//go:build integration

package postgres

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestApplyPatch_ConcurrentLockVsProfile_VersionCAS verifies that two concurrent
// ApplyPatch calls starting from the same version=1 snapshot serialize correctly:
// exactly one succeeds and advances the row to version=2, while the other returns
// ErrAuthConcurrentUpdate. The final row reflects only the winner's change.
//
// Concurrency note: this test relies on PG READ COMMITTED row-lock
// serialization. Both goroutines BEGIN+UPDATE concurrently; PG places
// a write lock on the row, the second UPDATE waits, then re-evaluates
// the WHERE version=$N predicate against the committed value and
// finds zero rows affected → ErrAuthConcurrentUpdate. The barrier
// only raises the collision probability — version CAS correctness
// does not require deterministic ordering.
func TestApplyPatch_ConcurrentLockVsProfile_VersionCAS(t *testing.T) {
	pool := setupPGPool(t)
	ctx := context.Background()

	userRepo, err := NewPGUserRepository(pool.DB())
	require.NoError(t, err)

	txm := adapterpg.NewTxManager(pool)

	// Create a base user with version=1.
	suffix := uuid.NewString()[:8]
	baseUser := newTestUser("casuser-"+suffix, "casuser-"+suffix+"@test.example")
	require.NoError(t, userRepo.Create(ctx, baseUser))

	// Snapshot version=1.
	snapshot, err := userRepo.GetByID(ctx, baseUser.ID)
	require.NoError(t, err)
	require.Equal(t, int64(1), snapshot.Version)

	// barrier ensures both goroutines read the snapshot before either patches.
	var barrier sync.WaitGroup
	barrier.Add(2)

	type result struct {
		user *domain.User
		err  error
	}
	resultsCh := make(chan result, 2)

	// Goroutine A: lock the user (status → locked).
	go func() {
		// Signal ready to patch.
		barrier.Done()
		barrier.Wait() // wait for both goroutines to be ready.

		var (
			u   *domain.User
			err error
		)
		txErr := txm.RunInTx(ctx, func(txCtx context.Context) error {
			locked := domain.StatusLocked
			u, err = userRepo.ApplyPatch(txCtx, ports.UserPatch{
				ID:             snapshot.ID,
				Status:         &locked,
				UpdatedAt:      time.Now().UTC(),
				CurrentVersion: snapshot.Version,
			})
			return err
		})
		if txErr != nil {
			resultsCh <- result{err: txErr}
			return
		}
		resultsCh <- result{user: u}
	}()

	// Goroutine B: rename the user (username → "newname-<suffix>").
	go func() {
		// Signal ready to patch.
		barrier.Done()
		barrier.Wait() // wait for both goroutines to be ready.

		newName := "newname-" + suffix
		var (
			u   *domain.User
			err error
		)
		txErr := txm.RunInTx(ctx, func(txCtx context.Context) error {
			u, err = userRepo.ApplyPatch(txCtx, ports.UserPatch{
				ID:             snapshot.ID,
				Username:       &newName,
				UpdatedAt:      time.Now().UTC(),
				CurrentVersion: snapshot.Version,
			})
			return err
		})
		if txErr != nil {
			resultsCh <- result{err: txErr}
			return
		}
		resultsCh <- result{user: u}
	}()

	r1 := <-resultsCh
	r2 := <-resultsCh

	successCount := 0
	casErrorCount := 0
	for _, r := range []result{r1, r2} {
		if r.err == nil {
			successCount++
			continue
		}
		var ec *errcode.Error
		require.ErrorAs(t, r.err, &ec, "unexpected error type: %v", r.err)
		assert.Equal(t, errcode.ErrAuthConcurrentUpdate, ec.Code,
			"loser must return ErrAuthConcurrentUpdate")
		casErrorCount++
	}

	assert.Equal(t, 1, successCount, "exactly one concurrent ApplyPatch must succeed")
	assert.Equal(t, 1, casErrorCount, "exactly one concurrent ApplyPatch must return ErrAuthConcurrentUpdate")

	// Final row must be at version=2 (exactly one write committed).
	final, err := userRepo.GetByID(ctx, snapshot.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(2), final.Version, "final version must be 2 (exactly one write)")

	// The final state must be exactly one of the two mutations, not both.
	isLocked := final.Status == domain.StatusLocked
	isRenamed := final.Username == "newname-"+suffix
	// Exactly one winner: XOR.
	assert.True(t, isLocked != isRenamed,
		"final state must reflect exactly one writer: status=%s username=%s",
		final.Status, final.Username)
}
