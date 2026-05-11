//go:build integration

// Package identitymanage — PG integration test for ChangePassword concurrent CAS semantics.
//
// Build tag: integration. Run with: go test -tags=integration ./...
// Not included in the default go test ./... run.
//
// Rationale: The mem-store concurrent test (TestChangePassword_ConcurrentRequests_ExactlyOneSucceeds)
// proves CAS logic correctness against in-memory repo. This test proves the same
// invariant against a real PostgreSQL instance using testcontainers, verifying that
// the SQL CAS guard (WHERE password_version=$expected) delivers exactly-once semantics
// under concurrent goroutine load.
package identitymanage

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"

	adapterpg "github.com/ghbvf/gocell/adapters/postgres"
	accesspgrepo "github.com/ghbvf/gocell/cells/accesscore/internal/adapters/postgres"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
	globaltestutil "github.com/ghbvf/gocell/tests/testutil"
)

// pgIntegMigrationsFS returns the shared adapters/postgres migration FS.
// Duplicate of adapters/postgres test helper — needed because _test.go files
// cannot be imported across packages.
func pgIntegMigrationsFS(t testing.TB) fs.FS {
	t.Helper()
	fsys, err := adapterpg.MigrationsFS()
	require.NoError(t, err)
	return fsys
}

// setupIdentityManagePG starts a PostgreSQL testcontainer, applies all migrations,
// and returns a PGUserRepo + TxManager + cleanup func.
func setupIdentityManagePG(t *testing.T) (*accesspgrepo.PGUserRepo, *adapterpg.TxManager, func()) {
	t.Helper()
	globaltestutil.RequireDocker(t)

	ctx := context.Background()

	container, err := tcpostgres.Run(ctx, globaltestutil.PostgresImage,
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	require.NoError(t, err, "failed to start postgres container")

	connStr, err := container.ConnectionString(ctx, "sslmode=disable")
	require.NoError(t, err)

	pool, err := adapterpg.NewPool(ctx, adapterpg.Config{DSN: connStr})
	require.NoError(t, err)

	migrator, err := adapterpg.NewMigrator(pool, pgIntegMigrationsFS(t), "schema_migrations")
	require.NoError(t, err)
	require.NoError(t, migrator.Up(ctx), "migrations must apply cleanly")

	txMgr := adapterpg.NewTxManager(pool)
	repo, err := accesspgrepo.NewPGUserRepo(pool.DB(), txMgr, clock.Real())
	require.NoError(t, err)

	cleanup := func() {
		if err := pool.Close(ctx); err != nil {
			t.Logf("WARN: pool close: %v", err)
		}
		if err := container.Terminate(ctx); err != nil {
			t.Logf("WARN: failed to terminate postgres container: %v", err)
		}
	}

	return repo, txMgr, cleanup
}

// newPGIntegRefreshStore returns an in-memory refresh.Store for use in PG integration tests.
// Refresh token revocation is in-memory in both tests and production (separate store);
// the CAS guard under test lives entirely in the user table via PGUserRepo.
func newPGIntegRefreshStore() refresh.Store {
	clk := storetest.NewFakeClock(time.Now())
	store, err := refreshmem.New(refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         time.Hour,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}, clk, nil)
	if err != nil {
		panic("pg integration test setup: " + err.Error())
	}
	return store
}

// pgStubTokenIssuer is a minimal stub for the TokenIssuer interface. The PG
// concurrent test only needs to verify CAS semantics, not token content.
type pgStubTokenIssuer struct {
	pair dto.TokenPair
}

func (s *pgStubTokenIssuer) IssueForUser(_ context.Context, _ string) (dto.TokenPair, error) {
	return s.pair, nil
}

// TestChangePassword_ConcurrentRequests_ExactlyOneSucceeds_PG verifies that when
// two goroutines race to change the same user's password against a real PostgreSQL
// database, exactly one succeeds and the other receives ErrVersionConflict.
//
// This test is the real-DB counterpart of TestChangePassword_ConcurrentRequests_ExactlyOneSucceeds
// (mem path). It exercises the SQL CAS guard:
//
//	UPDATE users SET password_hash=..., password_version=password_version+1, ...
//	WHERE id=$1 AND password_version=$2
//	RETURNING password_version
//
// Both goroutines read user.PasswordVersion=0 from the same snapshot, hash their
// respective new passwords, and race to the UPDATE. PostgreSQL row-level locking
// serialises the two writes: the winner's RETURNING returns the new version, the
// loser gets 0 rows → ErrVersionConflict (HTTP 409).
func TestChangePassword_ConcurrentRequests_ExactlyOneSucceeds_PG(t *testing.T) {
	repo, txMgr, cleanup := setupIdentityManagePG(t)
	defer cleanup()

	ctx := context.Background()

	// Seed a user with a known password.
	oldPassword := "old-secure-password-123"
	hash, err := bcrypt.GenerateFromPassword([]byte(oldPassword), bcrypt.MinCost)
	require.NoError(t, err)

	user := &domain.User{
		ID:                    uuid.NewString(),
		Username:              "pg-cas-race-user",
		Email:                 "pg-cas-race@example.com",
		PasswordHash:          string(hash),
		PasswordVersion:       0,
		PasswordResetRequired: false,
		Status:                domain.StatusActive,
		CreationSource:        domain.UserSourceIdentity,
		CreatedAt:             time.Now().UTC().Truncate(time.Millisecond),
		UpdatedAt:             time.Now().UTC().Truncate(time.Millisecond),
	}
	require.NoError(t, repo.Create(ctx, user))

	stub := &pgStubTokenIssuer{pair: dto.TokenPair{AccessToken: "at-pg", RefreshToken: "rt-pg"}}
	svc, err := NewService(
		repo,
		testutil.RealSessionRepo(t),
		newPGIntegRefreshStore(),
		slog.Default(),
		WithTokenIssuer(stub),
		WithClock(clock.Real()),
		WithTxManager(txMgr),
	)
	require.NoError(t, err)

	// Use a mem.SessionRepository to satisfy the session revoke path inside the tx.
	// (PG session repo is not required for this CAS-focused test.)
	_ = mem.NewSessionRepository(clock.Real()) // already injected via testutil.RealSessionRepo above

	type result struct{ err error }
	results := make(chan result, 2)

	var wg sync.WaitGroup
	wg.Add(2)
	for i := 0; i < 2; i++ {
		newPw := "new-password-goroutine-A"
		if i == 1 {
			newPw = "new-password-goroutine-B"
		}
		go func(newPw string) {
			defer wg.Done()
			_, cerr := svc.ChangePassword(context.Background(), ChangePasswordInput{
				UserID:      user.ID,
				OldPassword: oldPassword,
				NewPassword: newPw,
			})
			results <- result{cerr}
		}(newPw)
	}

	wg.Wait()
	close(results)

	var (
		successes        int32
		versionConflicts int32
		loginFailures    int32
	)
	for r := range results {
		if r.err == nil {
			atomic.AddInt32(&successes, 1)
		} else {
			var ce *errcode.Error
			if errors.As(r.err, &ce) && ce.Code == errcode.ErrVersionConflict {
				atomic.AddInt32(&versionConflicts, 1)
			} else {
				atomic.AddInt32(&loginFailures, 1)
			}
		}
	}

	// CAS semantics: exactly one goroutine must succeed and the other must
	// receive ErrVersionConflict. Any loginFailure indicates a test defect.
	if loginFailures > 0 {
		t.Fatalf("unexpected loginFailure(s) in concurrent PG ChangePassword test: successes=%d versionConflicts=%d loginFailures=%d",
			successes, versionConflicts, loginFailures)
	}
	assert.Equal(t, int32(1), successes, "exactly one concurrent ChangePassword must succeed (PG)")
	assert.Equal(t, int32(1), versionConflicts, "exactly one concurrent ChangePassword must yield ErrVersionConflict (PG)")

	// Version must have advanced exactly once.
	got, err := repo.GetByID(ctx, user.ID)
	require.NoError(t, err)
	assert.Equal(t, int64(1), got.PasswordVersion, "password_version must be exactly 1 after exactly one success (PG)")
}
