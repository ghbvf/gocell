package accesscoretest_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/accesscore/accesscoretest"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/cells/accesscore/slices/identitymanage"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// testPasswordPlain is the canonical test password used to generate testBcryptHash.
const testPasswordPlain = "test-password"

// testBcryptHash is a bcrypt hash of testPasswordPlain, lazily generated once
// per test binary execution. Using a real hash (not a fake literal) ensures
// tests that exercise password verification paths work correctly.
var (
	testBcryptHashOnce sync.Once
	testBcryptHash     string
)

func getTestBcryptHash() string {
	testBcryptHashOnce.Do(func() {
		h, err := bcrypt.GenerateFromPassword([]byte(testPasswordPlain), bcrypt.MinCost)
		if err != nil {
			panic("accesscoretest_test: failed to generate test bcrypt hash: " + err.Error())
		}
		testBcryptHash = string(h)
	})
	return testBcryptHash
}

// ---- FakeConfigGetter ----

func TestFakeConfigGetter_StubReturn(t *testing.T) {
	t.Parallel()
	stubs := map[string]accesscoretest.ConfigGetterStub{
		"jwt.ttl": {
			Entry: &ports.ConfigEntry{Key: "jwt.ttl", Value: "3600", Version: 2},
		},
	}
	g := accesscoretest.NewFakeConfigGetter(stubs)

	entry, err := g.GetEntry(context.Background(), "jwt.ttl")
	require.NoError(t, err)
	assert.Equal(t, "jwt.ttl", entry.Key)
	assert.Equal(t, "3600", entry.Value)
	assert.Equal(t, 2, entry.Version)

	// Calls recorded in order.
	calls := g.Calls()
	require.Len(t, calls, 1)
	assert.Equal(t, "jwt.ttl", calls[0])

	// Second call appended.
	_, _ = g.GetEntry(context.Background(), "jwt.ttl")
	assert.Len(t, g.Calls(), 2)
}

func TestFakeConfigGetter_Reset(t *testing.T) {
	t.Parallel()
	g := accesscoretest.NewFakeConfigGetter(nil)
	_, _ = g.GetEntry(context.Background(), "any")
	assert.Len(t, g.Calls(), 1)
	g.Reset()
	assert.Empty(t, g.Calls())
}

func TestFakeConfigGetter_DefaultErrConfigNotFound(t *testing.T) {
	t.Parallel()
	g := accesscoretest.NewFakeConfigGetter(nil)
	_, err := g.GetEntry(context.Background(), "missing.key")
	require.Error(t, err)
	assert.True(t, errcode.IsDomainNotFound(err, errcode.ErrConfigNotFound, errcode.ErrConfigRepoNotFound),
		"expected config not found, got: %v", err)
}

func TestFakeConfigGetter_StubErr(t *testing.T) {
	t.Parallel()
	wantErr := errcode.New(errcode.KindUnavailable, errcode.ErrConfigNotFound, "transient")
	g := accesscoretest.NewFakeConfigGetter(map[string]accesscoretest.ConfigGetterStub{
		"key": {Err: wantErr},
	})
	_, err := g.GetEntry(context.Background(), "key")
	assert.ErrorIs(t, err, wantErr)
}

// ---- FakeUserRepo ----

func newTestUser(t *testing.T, id, username string) domain.User {
	t.Helper()
	u, err := domain.NewUser(username, username+"@test.com", getTestBcryptHash(), time.Now().UTC())
	require.NoError(t, err)
	u.ID = id
	return *u
}

func TestFakeUserRepo_RoundTrip(t *testing.T) {
	t.Parallel()
	r := accesscoretest.NewFakeUserRepo()
	ctx := context.Background()
	u := newTestUser(t, "u1", "alice")

	// Seed then read back.
	r.SeedUser(u)
	got, err := r.GetByID(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, "u1", got.ID)
	assert.Equal(t, "alice", got.Username)

	// Update.
	got.Username = "alice2"
	require.NoError(t, r.Update(ctx, got))
	updated, err := r.GetByID(ctx, "u1")
	require.NoError(t, err)
	assert.Equal(t, "alice2", updated.Username)

	// Delete.
	require.NoError(t, r.Delete(ctx, "u1"))
	_, err = r.GetByID(ctx, "u1")
	require.Error(t, err)

	// Snapshot after delete should be empty.
	assert.Empty(t, r.Snapshot())
}

func TestFakeUserRepo_Create(t *testing.T) {
	t.Parallel()
	r := accesscoretest.NewFakeUserRepo()
	u := newTestUser(t, "u2", "bob")
	require.NoError(t, r.Create(context.Background(), &u))
	snap := r.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, "u2", snap[0].ID)
}

func TestFakeUserRepo_GetByUsername(t *testing.T) {
	t.Parallel()
	r := accesscoretest.NewFakeUserRepo()
	u := newTestUser(t, "u3", "carol")
	r.SeedUser(u)
	got, err := r.GetByUsername(context.Background(), "carol")
	require.NoError(t, err)
	assert.Equal(t, "u3", got.ID)
}

func TestFakeUserRepo_CallsOf(t *testing.T) {
	t.Parallel()
	r := accesscoretest.NewFakeUserRepo()
	u := newTestUser(t, "u4", "dave")
	r.SeedUser(u)

	ctx := context.Background()
	_, _ = r.GetByID(ctx, "u4")
	_, _ = r.GetByID(ctx, "u4")
	_, _ = r.GetByUsername(ctx, "dave")

	assert.Len(t, r.CallsOf("GetByID"), 2)
	assert.Len(t, r.CallsOf("GetByUsername"), 1)
	assert.Empty(t, r.CallsOf("Delete"))
}

func TestFakeUserRepo_NotFound(t *testing.T) {
	t.Parallel()
	r := accesscoretest.NewFakeUserRepo()
	_, err := r.GetByID(context.Background(), "nonexistent")
	require.Error(t, err)
	var ec *errcode.Error
	assert.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.KindNotFound, ec.Kind)
}

func TestFakeUserRepo_BumpAuthzEpoch(t *testing.T) {
	t.Parallel()
	r := accesscoretest.NewFakeUserRepo()
	u := newTestUser(t, "u5", "eve")
	r.SeedUser(u)

	newEpoch, err := r.BumpAuthzEpoch(context.Background(), "u5")
	require.NoError(t, err)
	assert.Equal(t, int64(2), newEpoch)

	got, _ := r.GetByID(context.Background(), "u5")
	assert.Equal(t, int64(2), got.AuthzEpoch())
}

// ---- FakeRoleRepo ----

func TestFakeRoleRepo_AssignToUser(t *testing.T) {
	t.Parallel()
	r := accesscoretest.NewFakeRoleRepo()
	ctx := context.Background()

	// First assign: changed=true.
	changed, err := r.AssignToUser(ctx, "u1", "admin")
	require.NoError(t, err)
	assert.True(t, changed)

	// Idempotent repeat: changed=false.
	changed, err = r.AssignToUser(ctx, "u1", "admin")
	require.NoError(t, err)
	assert.False(t, changed)

	snap := r.Snapshot()
	require.Contains(t, snap, "u1")
	assert.Contains(t, snap["u1"], "admin")
}

func TestFakeRoleRepo_SeedAssignment(t *testing.T) {
	t.Parallel()
	r := accesscoretest.NewFakeRoleRepo()
	r.SeedAssignment("u1", "editor")

	snap := r.Snapshot()
	assert.Contains(t, snap["u1"], "editor")
}

func TestFakeRoleRepo_CallsOf(t *testing.T) {
	t.Parallel()
	r := accesscoretest.NewFakeRoleRepo()
	ctx := context.Background()

	_, _ = r.GetByUserID(ctx, "u1")
	_, _ = r.GetByUserID(ctx, "u2")
	_, _ = r.CountByRole(ctx, "admin")

	assert.Len(t, r.CallsOf("GetByUserID"), 2)
	assert.Len(t, r.CallsOf("CountByRole"), 1)
}

func TestFakeRoleRepo_GetByID_NotFound(t *testing.T) {
	t.Parallel()
	r := accesscoretest.NewFakeRoleRepo()
	_, err := r.GetByID(context.Background(), "nonexistent")
	require.Error(t, err)
	var ec *errcode.Error
	assert.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.KindNotFound, ec.Kind)
}

// ---- BuildConfigReceiveService ----

func TestBuildConfigReceiveService_DefaultWiring(t *testing.T) {
	t.Parallel()
	svc := accesscoretest.BuildConfigReceiveService(t)
	require.NotNil(t, svc)
}

func TestBuildConfigReceiveService_WithCustomGetter(t *testing.T) {
	t.Parallel()
	g := accesscoretest.NewFakeConfigGetter(map[string]accesscoretest.ConfigGetterStub{
		"key": {Entry: &ports.ConfigEntry{Key: "key", Value: "v"}},
	})
	svc := accesscoretest.BuildConfigReceiveService(t, accesscoretest.WithReceiveConfigGetter(g))
	require.NotNil(t, svc)
	// Smoke: the service exists and can receive the custom getter.
}

// ---- NewCredentialInvalidator ----

func TestNewCredentialInvalidator_DefaultWiring(t *testing.T) {
	t.Parallel()
	// Should not panic.
	inv := accesscoretest.NewCredentialInvalidator(t)
	require.NotNil(t, inv)
	// Calling Apply on an empty FakeUserRepo returns a not-found error (not a panic).
	err := inv.Apply(context.Background(), "nonexistent-user", 0)
	require.Error(t, err)
}

// ---- BuildIdentityManageService ----

func TestBuildIdentityManageService_DefaultWiring(t *testing.T) {
	t.Parallel()
	svc, userRepo, rec := accesscoretest.BuildIdentityManageService(t)
	require.NotNil(t, svc)
	require.NotNil(t, userRepo)
	require.NotNil(t, rec)
}

func TestBuildIdentityManageService_CreateEndToEnd(t *testing.T) {
	t.Parallel()
	svc, userRepo, rec := accesscoretest.BuildIdentityManageService(t)

	// Inject a principal so actorFromContext succeeds.
	ctx := auth.TestContext("admin-user-id", []string{auth.RoleAdmin})

	input := identitymanage.CreateInput{
		Username: "newuser",
		Email:    "newuser@test.com",
		Password: "correct-horse-battery-staple",
	}
	user, err := svc.Create(ctx, input)
	require.NoError(t, err)
	assert.Equal(t, "newuser", user.Username)

	// FakeUserRepo should contain the new user.
	snap := userRepo.Snapshot()
	require.Len(t, snap, 1)
	assert.Equal(t, "newuser", snap[0].Username)

	// Recorder should have captured the UserCreated event.
	entries := rec.EntriesByType(identitymanage.TopicUserCreated)
	assert.Len(t, entries, 1)
}

func TestBuildIdentityManageService_Create_TableDriven(t *testing.T) {
	t.Parallel()
	adminCtx := auth.TestContext("admin-user-id", []string{auth.RoleAdmin})

	cases := []struct {
		name    string
		input   identitymanage.CreateInput
		wantErr bool
	}{
		{
			name: "happy path",
			input: identitymanage.CreateInput{
				Username: "validuser",
				Email:    "valid@test.com",
				Password: "strong-password-123",
			},
			wantErr: false,
		},
		{
			name: "missing username",
			input: identitymanage.CreateInput{
				Username: "",
				Email:    "valid@test.com",
				Password: "strong-password-123",
			},
			wantErr: true,
		},
		{
			name: "missing password",
			input: identitymanage.CreateInput{
				Username: "validuser2",
				Email:    "valid2@test.com",
				Password: "",
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			svc, _, _ := accesscoretest.BuildIdentityManageService(t)
			_, err := svc.Create(adminCtx, tc.input)
			if tc.wantErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestFakeRoleRepo_RemoveFromUserIfNotLast(t *testing.T) {
	t.Parallel()

	t.Run("removes non-last assignment", func(t *testing.T) {
		t.Parallel()
		r := accesscoretest.NewFakeRoleRepo()
		ctx := context.Background()
		// Two admins: removal should succeed.
		r.SeedAssignment("u1", auth.RoleAdmin)
		r.SeedAssignment("u2", auth.RoleAdmin)

		removed, err := r.RemoveFromUserIfNotLast(ctx, "u1", auth.RoleAdmin)
		require.NoError(t, err)
		assert.True(t, removed)
		snap := r.Snapshot()
		assert.NotContains(t, snap["u1"], auth.RoleAdmin)
	})

	t.Run("refuses last effective admin removal", func(t *testing.T) {
		t.Parallel()
		r := accesscoretest.NewFakeRoleRepo()
		ctx := context.Background()
		// Only one admin: removal must be refused.
		r.SeedAssignment("u1", auth.RoleAdmin)

		removed, err := r.RemoveFromUserIfNotLast(ctx, "u1", auth.RoleAdmin)
		require.Error(t, err)
		assert.False(t, removed)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.KindPermissionDenied, ec.Kind)
	})

	t.Run("idempotent removal of absent assignment", func(t *testing.T) {
		t.Parallel()
		r := accesscoretest.NewFakeRoleRepo()
		ctx := context.Background()

		removed, err := r.RemoveFromUserIfNotLast(ctx, "u1", "editor")
		require.NoError(t, err)
		assert.False(t, removed)
	})
}
