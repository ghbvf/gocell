package sessionlogin

// C1/C2 security normalization tests.
//
// RED phase: these tests assert post-fix invariants that do NOT pass yet:
//   1. missing-user / bad-password / inactive → same code + message + Kind (401)
//   2. inactive path executes bcrypt compare (no timing bypass)
//   3. errMsgInvalidCredentials const covers all three paths
//
// After the service.go fix they turn GREEN.

import (
	"context"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// securityTestBcryptCost is the bcrypt cost used for seeding test user hashes in
// security tests. It mirrors domain.BcryptCost so that timing-normalization tests
// reflect the same hash cost relationship as production: dummyBcryptHash (cost=12)
// vs user hash (cost=12). Using a lower cost would invert the relative timing
// (dummy slower than real hash), making the tests misleading about the actual
// timing oracle risk.
//
// Accept the associated test slowdown (~250 ms per bcrypt call) as the correct
// tradeoff: correctness of timing assertions outweighs test speed.
const securityTestBcryptCost = domain.BcryptCost

// countingComparer is a test-only passwordComparer that counts how many times
// it is called and delegates to bcrypt.CompareHashAndPassword.
type countingComparer struct {
	calls atomic.Int64
}

func (c *countingComparer) compare(hash, password []byte) error {
	c.calls.Add(1)
	return bcrypt.CompareHashAndPassword(hash, password)
}

// seedInactiveUser creates a user with the given status and returns it.
func seedInactiveUser(t *testing.T, repo *mem.UserRepository, username, password string, status domain.UserStatus) *domain.User {
	t.Helper()
	hash, err := bcrypt.GenerateFromPassword([]byte(password), securityTestBcryptCost)
	require.NoError(t, err)
	u, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:           "usr-" + username,
		Username:     username,
		Email:        username + "@test.com",
		PasswordHash: string(hash),
		Status:       status,
		Source:       domain.UserSourceIdentity,
		AuthzEpoch:   1,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	})
	require.NoError(t, err)
	require.NoError(t, repo.Create(context.Background(), u))
	return u
}

// newTestServiceWithComparer constructs a Service with a custom comparer injected.
func newTestServiceWithComparer(t *testing.T, cmp *countingComparer) (*Service, *mem.UserRepository) {
	t.Helper()
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	sessionStore := testutil.RealSessionRepo(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	svc := MustNewService(
		userRepo, sessionStore, roleRepo, newTestRefreshStore(),
		testIssuer, slog.Default(),
		WithClock(clock.Real()),
		WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
		WithSessionTTL(time.Hour),
		withPasswordComparer(cmp.compare),
	)
	return svc, userRepo
}

// TestLogin_ErrorPathUniformity asserts that missing-user, bad-password, and
// inactive-user all return the SAME error code, message, and HTTP Kind (401).
// This prevents account-existence enumeration via differing error codes or messages.
func TestLogin_ErrorPathUniformity(t *testing.T) {
	t.Parallel()

	cmp := &countingComparer{}
	svc, userRepo := newTestServiceWithComparer(t, cmp)

	// Seed an active user and an inactive (locked) user with the same password.
	seedUser(userRepo, "active-uniform", "correct-pass")
	seedInactiveUser(t, userRepo, "locked-uniform", "correct-pass", domain.StatusLocked)

	cases := []struct {
		name  string
		input LoginInput
	}{
		{
			name:  "missing user",
			input: LoginInput{Username: "ghost-no-exist", Password: "any-pass"},
		},
		{
			name:  "bad password",
			input: LoginInput{Username: "active-uniform", Password: "wrong-pass"},
		},
		{
			name:  "inactive (locked) user",
			input: LoginInput{Username: "locked-uniform", Password: "correct-pass"},
		},
	}

	var firstCode errcode.Code
	var firstMsg string
	var firstKind errcode.Kind

	for i, tc := range cases {
		_, err := svc.Login(context.Background(), tc.input)
		require.Error(t, err, "case %q must return an error", tc.name)

		var ec *errcode.Error
		require.ErrorAs(t, err, &ec, "case %q must return *errcode.Error", tc.name)

		if i == 0 {
			firstCode = ec.Code
			firstMsg = ec.Message
			firstKind = ec.Kind
		} else {
			assert.Equal(t, firstCode, ec.Code,
				"case %q: error code must match %q (防枚举 — all failure paths uniform)", tc.name, firstCode)
			assert.Equal(t, firstMsg, ec.Message,
				"case %q: error message must match %q (防枚举 — const literal, no account-state leak)", tc.name, firstMsg)
			assert.Equal(t, firstKind, ec.Kind,
				"case %q: error Kind must match %v (same HTTP status, 401)", tc.name, firstKind)
		}

		// All paths must yield KindUnauthenticated (→ HTTP 401).
		assert.Equal(t, errcode.KindUnauthenticated, ec.Kind,
			"case %q: all login failure paths must yield KindUnauthenticated (401)", tc.name)
		assert.Equal(t, errcode.ErrAuthLoginFailed, ec.Code,
			"case %q: all login failure paths must yield ErrAuthLoginFailed", tc.name)
	}
}

// TestLogin_InactivePath_BcryptExecuted asserts that when a locked/suspended
// user attempts login, bcrypt comparison IS executed (no timing bypass).
// A spy comparer counts how many times it is called.
func TestLogin_InactivePath_BcryptExecuted(t *testing.T) {
	t.Parallel()

	cmp := &countingComparer{}
	svc, userRepo := newTestServiceWithComparer(t, cmp)

	// Seed a locked user (inactive, non-authenticatable).
	seedInactiveUser(t, userRepo, "locked-bcrypt-spy", "some-pass", domain.StatusLocked)

	_, err := svc.Login(context.Background(), LoginInput{
		Username: "locked-bcrypt-spy",
		Password: "some-pass",
	})
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthLoginFailed, ec.Code)

	// The comparer must have been called at least once to prevent timing sidechannel.
	assert.GreaterOrEqual(t, cmp.calls.Load(), int64(1),
		"bcrypt compare must be executed even for inactive users (timing normalization)")
}

// TestLogin_MissingUser_BcryptExecuted asserts that when no user is found,
// bcrypt comparison is still executed against a dummy hash (timing normalization).
func TestLogin_MissingUser_BcryptExecuted(t *testing.T) {
	t.Parallel()

	cmp := &countingComparer{}
	svc, _ := newTestServiceWithComparer(t, cmp)

	_, err := svc.Login(context.Background(), LoginInput{
		Username: "no-such-user",
		Password: "any-pass",
	})
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthLoginFailed, ec.Code)

	// The comparer must have been called for timing normalization.
	assert.GreaterOrEqual(t, cmp.calls.Load(), int64(1),
		"bcrypt compare must be executed even when user is not found (timing normalization)")
}

// TestLogin_InactivePath_NoPublic403 ensures the public login path never
// returns ErrAuthUserNotActive (which would reveal account status to unauthenticated callers).
func TestLogin_InactivePath_NoPublic403(t *testing.T) {
	t.Parallel()

	svc, userRepo := newTestService(t)
	seedInactiveUser(t, userRepo, "suspended-403-check", "pass", domain.StatusSuspended)

	_, err := svc.Login(context.Background(), LoginInput{
		Username: "suspended-403-check",
		Password: "pass",
	})
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)

	assert.NotEqual(t, errcode.ErrAuthUserNotActive, ec.Code,
		"public login must NOT return ErrAuthUserNotActive (account status enumeration)")
	assert.NotEqual(t, errcode.KindPermissionDenied, ec.Kind,
		"public login inactive path must NOT yield 403 KindPermissionDenied")
}

// TestLogin_InactiveInTx_NoPublic403 ensures the in-tx re-fetch also never
// returns ErrAuthUserNotActive on the public login path.
func TestLogin_InactiveInTx_NoPublic403(t *testing.T) {
	t.Parallel()

	// Use a racing repo where GetByUsername returns active (for pre-bcrypt pass)
	// but GetByUsernameForUpdate returns locked (concurrent deactivation race).
	hash, err := bcrypt.GenerateFromPassword([]byte("correct"), securityTestBcryptCost)
	require.NoError(t, err)

	activeUser, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:           "usr-race-active",
		Username:     "race-active",
		Email:        "race@test.com",
		PasswordHash: string(hash),
		Status:       domain.StatusActive,
		Source:       domain.UserSourceIdentity,
		AuthzEpoch:   1,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	})
	require.NoError(t, err)

	lockedUser, err := domain.ReconstituteUser(domain.ReconstituteUserParams{
		ID:           "usr-race-active",
		Username:     "race-active",
		Email:        "race@test.com",
		PasswordHash: string(hash),
		Status:       domain.StatusLocked,
		Source:       domain.UserSourceIdentity,
		AuthzEpoch:   1,
		CreatedAt:    time.Now(),
		UpdatedAt:    time.Now(),
	})
	require.NoError(t, err)

	baseRepo := mem.NewStore(clock.Real()).UserRepository()
	racingRepo := &versionRacingUserRepo{
		UserRepository: *baseRepo,
		preUser:        activeUser,
		lockedUser:     lockedUser,
	}

	sessionStore := testutil.RealSessionRepo(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	svc := MustNewService(
		racingRepo, sessionStore, roleRepo, newTestRefreshStore(),
		testIssuer, slog.Default(),
		WithClock(clock.Real()),
		WithTxManager(persistence.WrapForCell(&stubTxRunner{})),
		WithSessionTTL(time.Hour),
	)

	_, loginErr := svc.Login(context.Background(), LoginInput{
		Username: "race-active",
		Password: "correct",
	})
	require.Error(t, loginErr)

	var ec *errcode.Error
	require.ErrorAs(t, loginErr, &ec)

	assert.NotEqual(t, errcode.ErrAuthUserNotActive, ec.Code,
		"in-tx race path must NOT return ErrAuthUserNotActive (account status enumeration)")
	assert.Equal(t, errcode.ErrAuthLoginFailed, ec.Code,
		"in-tx inactive race path must return ErrAuthLoginFailed (401)")
}
