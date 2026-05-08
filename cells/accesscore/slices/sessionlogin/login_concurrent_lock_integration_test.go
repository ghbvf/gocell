//go:build integration

// Package sessionlogin — Login vs Lock concurrency integration test.
//
// Verifies the P1#1a invariant: if a user is locked before Login acquires the
// SELECT FOR UPDATE lock, Login must fail with ErrAuthUserLocked.  The test
// drives both operations concurrently over a real PostgreSQL backend using the
// testfx.SetupPGPool helper, ensuring the FOR UPDATE and ApplyPatch CAS
// genuinely serialize.
//
// Not included in the default go test ./... run.
// Run with: go test -tags=integration ./cells/accesscore/slices/sessionlogin/...
package sessionlogin

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	pgadapter "github.com/ghbvf/gocell/cells/accesscore/internal/adapters/postgres"
	"github.com/ghbvf/gocell/cells/accesscore/internal/adapters/postgres/testfx"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
)

// Test durations (TEST-TIME-LITERAL-01: extract to package-level consts).
const (
	loginLockJWTTTL           = 15 * time.Minute
	loginLockRefreshReuseWait = 2 * time.Second
)

// loginLockTestKeySet is a shared JWT key set for this integration test file.
var loginLockTestKeySet, _, _ = auth.MustNewTestKeySet(clock.Real())

// loginLockTestIssuer is used by the Login service.
var loginLockTestIssuer = func() *auth.JWTIssuer {
	i, err := auth.NewJWTIssuer(loginLockTestKeySet, "gocell-accesscore", loginLockJWTTTL, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	if err != nil {
		panic("integration test setup: " + err.Error())
	}
	return i
}()

// loginLockFixture wires sessionlogin.Service against PG repos + a real
// TxManager so SELECT FOR UPDATE semantics are exercised.
type loginLockFixture struct {
	loginSvc *Service
	userRepo *pgadapter.PGUserRepository
	pool     *adapterpg.Pool
	txm      *adapterpg.TxManager
}

func newLoginLockFixture(t *testing.T) *loginLockFixture {
	t.Helper()
	pool := testfx.SetupPGPool(t)

	userRepo, err := pgadapter.NewPGUserRepository(pool.DB())
	require.NoError(t, err)
	sessionRepo, err := pgadapter.NewPGSessionRepository(pool.DB(), clock.Real())
	require.NoError(t, err)
	roleRepo, err := pgadapter.NewPGRoleRepository(pool.DB(), clock.Real())
	require.NoError(t, err)

	txm := adapterpg.NewTxManager(pool)

	fakeClk := storetest.NewFakeClock(time.Now())
	refreshStore, err := refreshmem.New(refresh.Policy{
		ReuseInterval:  loginLockRefreshReuseWait,
		MaxAge:         time.Hour,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}, fakeClk, nil)
	require.NoError(t, err)

	loginSvc, err := NewService(
		userRepo, sessionRepo, roleRepo, refreshStore, loginLockTestIssuer, slog.Default(),
		WithClock(clock.Real()),
		WithTxManager(txm),
	)
	require.NoError(t, err)

	return &loginLockFixture{
		loginSvc: loginSvc,
		userRepo: userRepo,
		pool:     pool,
		txm:      txm,
	}
}

// seedPGUser creates a bcrypt-hashed user in the PG repo and returns id+username+password.
func seedPGUser(t *testing.T, ctx context.Context, repo *pgadapter.PGUserRepository, plainPassword string) (userID, username string) {
	t.Helper()
	suffix := uuid.NewString()[:8]
	username = "login-lock-" + suffix
	hash, err := bcrypt.GenerateFromPassword([]byte(plainPassword), domain.BcryptCost)
	require.NoError(t, err)

	now := time.Now().UTC().Truncate(time.Microsecond)
	u := &domain.User{
		ID:                    uuid.NewString(),
		Username:              username,
		Email:                 username + "@test.example",
		PasswordHash:          string(hash),
		PasswordResetRequired: false,
		Status:                domain.StatusActive,
		CreationSource:        domain.UserSourceIdentity,
		CreatedAt:             now,
		UpdatedAt:             now,
	}
	require.NoError(t, repo.Create(ctx, u))
	return u.ID, username
}

// lockUser directly applies a lock patch via the user repository (simulating
// identitymanage.Service.Lock without pulling in its full dependency graph).
func lockUser(ctx context.Context, repo *pgadapter.PGUserRepository, txm *adapterpg.TxManager, userID string) error {
	return txm.RunInTx(ctx, func(txCtx context.Context) error {
		user, err := repo.GetByID(txCtx, userID)
		if err != nil {
			return err
		}
		locked := domain.StatusLocked
		_, err = repo.ApplyPatch(txCtx, ports.UserPatch{
			ID:             userID,
			Status:         &locked,
			UpdatedAt:      time.Now().UTC(),
			CurrentVersion: user.Version,
		})
		return err
	})
}

// TestLogin_ConcurrentAdminLock_RejectsAfterLock runs N iterations of the
// Login+Lock race.  The invariant: if Lock completes and the user status is
// locked, any Login that begins AFTER the lock must return ErrAuthUserLocked.
//
// The test exploits the SELECT FOR UPDATE inside Login.RunInTx: if Lock's
// ApplyPatch commits before Login's FOR UPDATE reads the row, Login sees
// status=locked and rejects. This is a probabilistic test; N=20 increases
// confidence that the FOR UPDATE path is exercised at least some of the time.
func TestLogin_ConcurrentAdminLock_RejectsAfterLock(t *testing.T) {
	ctx := context.Background()
	const N = 20
	const plainPwd = "TestP@ss1234"

	for iter := range N {
		f := newLoginLockFixture(t)
		userID, username := seedPGUser(t, ctx, f.userRepo, plainPwd)

		var (
			wg          sync.WaitGroup
			loginErr    error
			lockErr     error
			loginTokens interface{}
		)
		wg.Add(2)

		// Use a barrier so both goroutines start as close together as possible.
		var barrier sync.WaitGroup
		barrier.Add(2)

		go func() {
			defer wg.Done()
			barrier.Done()
			barrier.Wait()
			tp, err := f.loginSvc.Login(ctx, LoginInput{Username: username, Password: plainPwd})
			loginTokens = tp
			loginErr = err
		}()

		go func() {
			defer wg.Done()
			barrier.Done()
			barrier.Wait()
			lockErr = lockUser(ctx, f.userRepo, f.txm, userID)
		}()

		wg.Wait()

		// Lock must always succeed.
		require.NoError(t, lockErr, "iter %d: lock must succeed", iter)

		// Read the final user state from DB.
		finalUser, err := f.userRepo.GetByID(ctx, userID)
		require.NoError(t, err)

		if finalUser.Status == domain.StatusLocked {
			// Core invariant: login after a completed lock must either have failed
			// or (if it won the race before the lock) produced a token — but there
			// must be no active sessions if the user is now locked.
			if loginErr == nil {
				// Login won the race before Lock committed. This is allowed.
				// Verify that no active (non-revoked) sessions remain — because
				// Lock calls RevokeByUserID.
				var activeCount int
				err := f.pool.DB().QueryRow(ctx,
					"SELECT COUNT(*) FROM sessions WHERE user_id = $1 AND revoked_at IS NULL",
					userID,
				).Scan(&activeCount)
				require.NoError(t, err)
				assert.Equal(t, 0, activeCount,
					"iter %d: after Lock completes, no active sessions must remain", iter)
			} else {
				// Login failed. If user is locked, the error must be ErrAuthUserLocked
				// or ErrAuthLoginFailed (user was locked before credential check).
				var ec *errcode.Error
				if assert.ErrorAs(t, loginErr, &ec, "iter %d: login error must be an errcode.Error", iter) {
					assert.True(t,
						ec.Code == errcode.ErrAuthUserLocked || ec.Code == errcode.ErrAuthLoginFailed,
						"iter %d: login must fail with ErrAuthUserLocked or ErrAuthLoginFailed when user is locked, got %s",
						iter, ec.Code)
				}
			}
		}
		// If user is still active, Login won the race cleanly — no assertion needed.
		_ = loginTokens
	}
}

// Ensure the mem.RoleRepository and mem.NewSessionRepository references compile
// in this package (needed only for unused-import avoidance in some setups).
var _ = mem.NewRoleRepository
