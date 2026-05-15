package mem

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

func TestUserRepo_PreservesPasswordResetRequired(t *testing.T) {
	ctx := context.Background()
	repo := NewStore(clock.Real()).UserRepository()

	user, err := domain.NewUser("testuser", "test@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-test-001"
	user.SetPasswordResetRequired(true, time.Now())
	user.CreationSource = domain.UserSourceSetup

	require.NoError(t, repo.Create(ctx, user))

	// GetByID should preserve the flag.
	got, err := repo.GetByID(ctx, "usr-test-001")
	require.NoError(t, err)
	assert.True(t, got.PasswordResetRequired(), "GetByID must preserve PasswordResetRequired")
	assert.Equal(t, domain.UserSourceSetup, got.CreationSource, "GetByID must preserve CreationSource")

	// GetByUsername should preserve the flag.
	got2, err := repo.GetByUsername(ctx, "testuser")
	require.NoError(t, err)
	assert.True(t, got2.PasswordResetRequired(), "GetByUsername must preserve PasswordResetRequired")
	assert.Equal(t, domain.UserSourceSetup, got2.CreationSource, "GetByUsername must preserve CreationSource")

	// Update should persist changes to the flag.
	got.SetPasswordResetRequired(false, time.Now())
	require.NoError(t, repo.Update(ctx, got))

	got3, err := repo.GetByID(ctx, "usr-test-001")
	require.NoError(t, err)
	assert.False(t, got3.PasswordResetRequired(), "Update must persist SetPasswordResetRequired(false)")
}

func TestUserRepo_UpdatePassword_Match(t *testing.T) {
	ctx := context.Background()
	repo := NewStore(clock.Real()).UserRepository()

	user, err := domain.NewUser("alice", "alice@example.com", "$2a$12$oldhash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-alice"
	user.PasswordVersion = 0
	require.NoError(t, repo.Create(ctx, user))

	newPV, err := repo.UpdatePassword(ctx, "usr-alice", "$2a$12$newhash", false, 0)
	require.NoError(t, err)
	assert.Equal(t, int64(1), newPV, "new password_version must be 1")

	got, err := repo.GetByID(ctx, "usr-alice")
	require.NoError(t, err)
	assert.Equal(t, "$2a$12$newhash", got.PasswordHash)
	assert.Equal(t, int64(1), got.PasswordVersion)
	assert.False(t, got.PasswordResetRequired())
}

func TestUserRepo_UpdatePassword_VersionMismatch(t *testing.T) {
	ctx := context.Background()
	repo := NewStore(clock.Real()).UserRepository()

	user, err := domain.NewUser("bob", "bob@example.com", "$2a$12$oldhash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-bob"
	user.PasswordVersion = 0
	require.NoError(t, repo.Create(ctx, user))

	// Provide stale version — should return ErrVersionConflict (KindConflict).
	_, err = repo.UpdatePassword(ctx, "usr-bob", "$2a$12$newhash", false, 99)
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindConflict, ce.Kind, "stale version must yield KindConflict")
	assert.Equal(t, errcode.ErrVersionConflict, ce.Code, "stale version must yield ErrVersionConflict")

	// Original hash must be unchanged.
	got, err := repo.GetByID(ctx, "usr-bob")
	require.NoError(t, err)
	assert.Equal(t, "$2a$12$oldhash", got.PasswordHash)
	assert.Equal(t, int64(0), got.PasswordVersion)
}

func TestUserRepo_UpdatePassword_NotFound(t *testing.T) {
	ctx := context.Background()
	repo := NewStore(clock.Real()).UserRepository()

	_, err := repo.UpdatePassword(ctx, "usr-nonexistent", "$2a$12$newhash", false, 0)
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindNotFound, ce.Kind)
	assert.Equal(t, errcode.ErrAuthUserNotFound, ce.Code)
}

func TestUserRepo_UpdatePassword_ResetRequiredFlag(t *testing.T) {
	ctx := context.Background()
	repo := NewStore(clock.Real()).UserRepository()

	user, err := domain.NewUser("carol", "carol@example.com", "$2a$12$oldhash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-carol"
	user.PasswordVersion = 0
	user.SetPasswordResetRequired(true, time.Now())
	require.NoError(t, repo.Create(ctx, user))

	// UpdatePassword with resetRequired=false must clear the flag.
	_, err = repo.UpdatePassword(ctx, "usr-carol", "$2a$12$newhash", false, 0)
	require.NoError(t, err)

	got, err := repo.GetByID(ctx, "usr-carol")
	require.NoError(t, err)
	assert.False(t, got.PasswordResetRequired(), "UpdatePassword must apply the resetRequired argument")
}

// ---------------------------------------------------------------------------
// BumpAuthzEpoch tests
// ---------------------------------------------------------------------------

func TestUserRepo_BumpAuthzEpoch_IncrementsAndReturns(t *testing.T) {
	ctx := context.Background()
	repo := NewStore(clock.Real()).UserRepository()

	user, err := domain.NewUser("epoch_user", "epoch@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-epoch-001"
	require.NoError(t, repo.Create(ctx, user))

	// First bump: 1 → 2 (initial epoch is 1 per S4d design).
	epoch1, err := repo.BumpAuthzEpoch(ctx, "usr-epoch-001")
	require.NoError(t, err)
	assert.Equal(t, int64(2), epoch1, "first BumpAuthzEpoch must return 2 (initial=1)")

	// Second bump: 2 → 3.
	epoch2, err := repo.BumpAuthzEpoch(ctx, "usr-epoch-001")
	require.NoError(t, err)
	assert.Equal(t, int64(3), epoch2, "second BumpAuthzEpoch must return 3")

	// GetByID must reflect the updated epoch.
	got, err := repo.GetByID(ctx, "usr-epoch-001")
	require.NoError(t, err)
	assert.Equal(t, int64(3), got.AuthzEpoch(), "GetByID must return updated AuthzEpoch")
}

func TestUserRepo_BumpAuthzEpoch_NotFound(t *testing.T) {
	ctx := context.Background()
	repo := NewStore(clock.Real()).UserRepository()

	_, err := repo.BumpAuthzEpoch(ctx, "usr-nonexistent")
	require.Error(t, err)
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindNotFound, ce.Kind)
	assert.Equal(t, errcode.ErrAuthUserNotFound, ce.Code)
}

func TestUserRepo_BumpAuthzEpoch_Concurrent(t *testing.T) {
	ctx := context.Background()
	repo := NewStore(clock.Real()).UserRepository()

	user, err := domain.NewUser("concurrent_epoch", "concurrent@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-concurrent-001"
	require.NoError(t, repo.Create(ctx, user))

	const goroutines = 100
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for range goroutines {
		go func() {
			defer wg.Done()
			_, _ = repo.BumpAuthzEpoch(ctx, "usr-concurrent-001")
		}()
	}
	wg.Wait()

	got, err := repo.GetByID(ctx, "usr-concurrent-001")
	require.NoError(t, err)
	assert.Equal(t, int64(goroutines)+1, got.AuthzEpoch(),
		"100 concurrent BumpAuthzEpoch calls starting from epoch=1 must result in epoch == 101")
}

// ---------------------------------------------------------------------------
// GetByIDForUpdate / GetByUsernameForUpdate fail-fast tests (E1)
// ---------------------------------------------------------------------------

// TestGetByIDForUpdate_NoAmbientTx is the RED conformance test for E1:
// calling GetByIDForUpdate without a mem-tx sentinel must fail with ErrInternal.
func TestGetByIDForUpdate_NoAmbientTx(t *testing.T) {
	store := NewStore(clock.Real())
	repo := store.UserRepository()

	user, err := domain.NewUser("forupdate", "forupdate@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-forupdate-001"
	require.NoError(t, repo.Create(context.Background(), user))

	// Call WITHOUT a mem-tx sentinel → must fail with ErrInternal.
	_, err = repo.GetByIDForUpdate(context.Background(), "usr-forupdate-001")
	require.Error(t, err, "GetByIDForUpdate must error without a mem-tx context")
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindInternal, ce.Kind)
	assert.Equal(t, errcode.ErrInternal, ce.Code)
	assert.Contains(t, ce.Message, "FOR UPDATE")
}

// TestGetByIDForUpdate_InsideRunInTx verifies that GetByIDForUpdate succeeds
// when called inside Store.TxRunner().RunInTx (the sentinel is injected).
func TestGetByIDForUpdate_InsideRunInTx(t *testing.T) {
	store := NewStore(clock.Real())
	repo := store.UserRepository()

	user, err := domain.NewUser("intx", "intx@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-intx-001"
	require.NoError(t, repo.Create(context.Background(), user))

	// Call INSIDE RunInTx → must succeed.
	var got *domain.User
	txErr := store.TxRunner().RunInTx(context.Background(), func(ctx context.Context) error {
		got, err = repo.GetByIDForUpdate(ctx, "usr-intx-001")
		return err
	})
	require.NoError(t, txErr)
	require.NotNil(t, got)
	assert.Equal(t, "usr-intx-001", got.ID)
}

// TestGetByUsernameForUpdate_NoAmbientTx is the RED conformance test for E1:
// calling GetByUsernameForUpdate without a mem-tx sentinel must fail with ErrInternal.
func TestGetByUsernameForUpdate_NoAmbientTx(t *testing.T) {
	store := NewStore(clock.Real())
	repo := store.UserRepository()

	user, err := domain.NewUser("forupdate-un", "forupdate-un@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-forupdate-un-001"
	require.NoError(t, repo.Create(context.Background(), user))

	// Call WITHOUT a mem-tx sentinel → must fail with ErrInternal.
	_, err = repo.GetByUsernameForUpdate(context.Background(), "forupdate-un")
	require.Error(t, err, "GetByUsernameForUpdate must error without a mem-tx context")
	var ce *errcode.Error
	require.ErrorAs(t, err, &ce)
	assert.Equal(t, errcode.KindInternal, ce.Kind)
	assert.Equal(t, errcode.ErrInternal, ce.Code)
	assert.Contains(t, ce.Message, "FOR UPDATE")
}

// TestGetByUsernameForUpdate_InsideRunInTx verifies that GetByUsernameForUpdate
// succeeds when called inside Store.TxRunner().RunInTx.
func TestGetByUsernameForUpdate_InsideRunInTx(t *testing.T) {
	store := NewStore(clock.Real())
	repo := store.UserRepository()

	user, err := domain.NewUser("intx-un", "intx-un@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-intx-un-001"
	require.NoError(t, repo.Create(context.Background(), user))

	// Call INSIDE RunInTx → must succeed.
	var got *domain.User
	txErr := store.TxRunner().RunInTx(context.Background(), func(ctx context.Context) error {
		got, err = repo.GetByUsernameForUpdate(ctx, "intx-un")
		return err
	})
	require.NoError(t, txErr)
	require.NotNil(t, got)
	assert.Equal(t, "usr-intx-un-001", got.ID)
}

// TestGetByIDForUpdate_WithTxContext verifies that mem.WithTxContext can be
// used by custom TxRunners to inject the sentinel for GetByIDForUpdate.
func TestGetByIDForUpdate_WithTxContext(t *testing.T) {
	store := NewStore(clock.Real())
	repo := store.UserRepository()

	user, err := domain.NewUser("withctx", "withctx@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-withctx-001"
	require.NoError(t, repo.Create(context.Background(), user))

	// WithTxContext must allow GetByIDForUpdate to succeed.
	ctx := WithTxContext(context.Background())
	got, err := repo.GetByIDForUpdate(ctx, "usr-withctx-001")
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.Equal(t, "usr-withctx-001", got.ID)
}

// ---------------------------------------------------------------------------
// R1: Single-lock model — no deadlock, serialization proof
// ---------------------------------------------------------------------------

// TestRunInTx_BumpAuthzEpoch_InsideTx verifies that BumpAuthzEpoch can be
// called inside a RunInTx closure without deadlocking. This is the critical
// case for credentialinvalidate.Apply (which calls BumpAuthzEpoch inside an
// ambient tx produced by rbacassign / identitymanage).
//
// Before the R1 fix, RunInTx held store.mu and BumpAuthzEpoch re-acquired it,
// causing an instant deadlock on sync.Mutex. This test would hang / fail with
// -timeout if the deadlock is reintroduced.
func TestRunInTx_BumpAuthzEpoch_InsideTx(t *testing.T) {
	store := NewStore(clock.Real())
	repo := store.UserRepository()

	user, err := domain.NewUser("epoch-intx", "epoch-intx@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-epoch-intx-001"
	require.NoError(t, repo.Create(context.Background(), user))

	var newEpoch int64
	txErr := store.TxRunner().RunInTx(context.Background(), func(txCtx context.Context) error {
		// GetByUsernameForUpdate + BumpAuthzEpoch both inside tx — must not deadlock.
		_, err := repo.GetByUsernameForUpdate(txCtx, "epoch-intx")
		if err != nil {
			return err
		}
		newEpoch, err = repo.BumpAuthzEpoch(txCtx, "usr-epoch-intx-001")
		return err
	})
	require.NoError(t, txErr)
	assert.Equal(t, int64(2), newEpoch, "epoch must be 2 after one bump (initial=1)")
}

// TestRunInTx_Serialization_ConcurrentBumpEpochBlocked verifies that the
// RunInTx write lock serializes concurrent BumpAuthzEpoch calls: one tx holds
// the lock while the second is blocked. After tx-1 commits, tx-2 sees the
// updated state. This proves the FOR-UPDATE-until-commit semantics.
//
// Specifically: tx-1 reads the user, bumps epoch to 2, sleeps, then commits.
// A concurrent BumpAuthzEpoch (outside tx) must block during the sleep and
// observe epoch=2 when it eventually acquires the lock (not epoch=1 which it
// would see if the lock were not held for the duration).
func TestRunInTx_Serialization_ConcurrentBumpEpochBlocked(t *testing.T) {
	store := NewStore(clock.Real())
	repo := store.UserRepository()

	user, err := domain.NewUser("serial-user", "serial@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-serial-001"
	require.NoError(t, repo.Create(context.Background(), user))

	// tx-1 signals that it has locked and is sleeping.
	locked := make(chan struct{})
	// tx-1 signals when it is done so the test can synchronize.
	done := make(chan struct{})

	// tx-1: hold the lock, bump epoch to 2, then signal.
	go func() {
		_ = store.TxRunner().RunInTx(context.Background(), func(txCtx context.Context) error {
			_, _ = repo.BumpAuthzEpoch(txCtx, "usr-serial-001") // epoch: 1 → 2
			close(locked)                                       // signal: lock held, proceed
			// Hold the lock for a short duration to let tx-2 start blocking.
			time.Sleep(20 * time.Millisecond)
			return nil
		})
		close(done)
	}()

	// Wait until tx-1 has the lock and has bumped the epoch.
	<-locked

	// tx-2 (outside RunInTx) calls BumpAuthzEpoch — must block until tx-1 releases.
	epoch2, err := repo.BumpAuthzEpoch(context.Background(), "usr-serial-001")
	<-done // tx-1 must have finished before us or concurrently with us

	require.NoError(t, err)
	// If serialization holds, tx-2 saw epoch=2 (set by tx-1) and bumped to 3.
	// Without the lock, tx-2 could have raced and produced epoch=2 too (duplicate).
	assert.Equal(t, int64(3), epoch2,
		"tx-2 BumpAuthzEpoch must see tx-1's committed epoch (2) and produce epoch=3, proving serialization")
}

// TestRunInTx_NoDeadlock_GetByUsernameForUpdateAndBump verifies that calling
// both GetByUsernameForUpdate AND BumpAuthzEpoch inside the same RunInTx
// closure does not deadlock. This is the exact pattern used in loginInTx.
func TestRunInTx_NoDeadlock_GetByUsernameForUpdateAndBump(t *testing.T) {
	store := NewStore(clock.Real())
	repo := store.UserRepository()

	user, err := domain.NewUser("nodeadlock", "nodeadlock@example.com", "$2a$12$hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-nodeadlock-001"
	require.NoError(t, repo.Create(context.Background(), user))

	done := make(chan error, 1)
	go func() {
		err := store.TxRunner().RunInTx(context.Background(), func(txCtx context.Context) error {
			// Mimic loginInTx: ForUpdate read then subsequent write.
			_, err := repo.GetByUsernameForUpdate(txCtx, "nodeadlock")
			if err != nil {
				return err
			}
			_, err = repo.BumpAuthzEpoch(txCtx, "usr-nodeadlock-001")
			return err
		})
		done <- err
	}()

	select {
	case err := <-done:
		require.NoError(t, err, "RunInTx with ForUpdate+BumpEpoch must not deadlock or fail")
	case <-time.After(2 * time.Second):
		t.Fatal("DEADLOCK DETECTED: RunInTx with ForUpdate+BumpAuthzEpoch blocked for >2s")
	}
}
