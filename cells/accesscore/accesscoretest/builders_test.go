// Package accesscoretest_test exercises the public testutil helpers exported by
// cells/accesscore/accesscoretest. Tests run TDD-first: they compile against the
// package being built and must pass with all implementations in place.
package accesscoretest_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/accesscoretest"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/slices/identitymanage"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
)

// TestNewAccessFixtureSeedAndQuery seeds a user and role, then reads them back
// via the repository accessors.
func TestNewAccessFixtureSeedAndQuery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := accesscoretest.NewAccessFixture(t, clock.Real())

	u, err := domain.NewUser("alice", "alice@example.com", "hash", clock.Real().Now())
	require.NoError(t, err)
	u.ID = "usr-alice"
	require.NoError(t, f.SeedUser(ctx, u))

	role := &domain.Role{ID: "role-viewer", Name: "Viewer"}
	require.NoError(t, f.SeedRole(ctx, role))

	// Read back via UserRepo.
	got, err := f.UserRepo().GetByID(ctx, u.ID)
	require.NoError(t, err)
	assert.Equal(t, u.ID, got.ID)
	assert.Equal(t, "alice", got.Username)

	// Read back via RoleRepo.
	gotRole, err := f.RoleRepo().GetByID(ctx, role.ID)
	require.NoError(t, err)
	assert.Equal(t, role.ID, gotRole.ID)
}

// TestAccessFixtureStorePaired asserts that UserRepo and RoleRepo share the same
// underlying store by verifying that a role assigned via f.SeedAssignment is
// visible through f.RoleRepo().GetByUserID.
func TestAccessFixtureStorePaired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := accesscoretest.NewAccessFixture(t, clock.Real())

	u, err := domain.NewUser("bob", "bob@example.com", "hash", clock.Real().Now())
	require.NoError(t, err)
	u.ID = "usr-bob"
	require.NoError(t, f.SeedUser(ctx, u))

	role := &domain.Role{ID: "role-admin", Name: "Admin"}
	require.NoError(t, f.SeedRole(ctx, role))

	// Assign via SeedAssignment (uses RoleRepo under the hood).
	require.NoError(t, f.SeedAssignment(ctx, u.ID, role.ID))

	// Verify cross-repo visibility: the assignment must be visible via RoleRepo.
	roles, err := f.RoleRepo().GetByUserID(ctx, u.ID)
	require.NoError(t, err)
	require.Len(t, roles, 1, "assignment seeded via SeedAssignment must be visible via RoleRepo")
	assert.Equal(t, role.ID, roles[0].ID)
}

// TestFakeConfigGetterHit exercises stub hit, stub error, and unknown-key paths.
func TestFakeConfigGetterHit(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// Stub hit — entry present.
	entry := &accesscoretest.ConfigGetterStub{
		Entry: &accesscoretest.FakeConfigEntry{Key: "foo", Value: "bar", Version: 1},
	}
	fg := accesscoretest.NewFakeConfigGetter(map[string]accesscoretest.ConfigGetterStub{
		"foo": *entry,
	})
	got, err := fg.GetEntry(ctx, "foo")
	require.NoError(t, err)
	assert.Equal(t, "foo", got.Key)
	assert.Equal(t, "bar", got.Value)

	// Stub with error.
	customErr := errcode.New(errcode.KindNotFound, errcode.ErrConfigNotFound, "config not found")
	fg2 := accesscoretest.NewFakeConfigGetter(map[string]accesscoretest.ConfigGetterStub{
		"missing": {Err: customErr},
	})
	_, err2 := fg2.GetEntry(ctx, "missing")
	require.Error(t, err2)
	assert.ErrorIs(t, err2, customErr)

	// Unknown key — returns ErrConfigNotFound by convention (documented in package).
	fg3 := accesscoretest.NewFakeConfigGetter(nil)
	_, err3 := fg3.GetEntry(ctx, "unknown")
	require.Error(t, err3, "unknown key must return an error")
}

// TestFakeConfigGetterCallsRecorded verifies that call order is captured.
func TestFakeConfigGetterCallsRecorded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	fg := accesscoretest.NewFakeConfigGetter(map[string]accesscoretest.ConfigGetterStub{
		"foo": {Entry: &accesscoretest.FakeConfigEntry{Key: "foo", Value: "v"}},
		"bar": {Entry: &accesscoretest.FakeConfigEntry{Key: "bar", Value: "v2"}},
	})
	_, _ = fg.GetEntry(ctx, "foo")
	_, _ = fg.GetEntry(ctx, "bar")

	calls := fg.Calls()
	require.Equal(t, []string{"foo", "bar"}, calls)

	fg.Reset()
	assert.Empty(t, fg.Calls(), "Reset must clear the call log")
}

// TestNewCredentialInvalidatorWithDefaults verifies that the helper returns a
// non-nil Invalidator with no options provided.
func TestNewCredentialInvalidatorWithDefaults(t *testing.T) {
	t.Parallel()
	inv := accesscoretest.NewCredentialInvalidator(t)
	require.NotNil(t, inv, "NewCredentialInvalidator must return non-nil *Invalidator")
}

// TestBuildIdentityManageServiceSmoke builds the service, seeds a user, creates
// another user through the service, then verifies the fixture state changed and
// the recorder captured a user-created event.
func TestBuildIdentityManageServiceSmoke(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	svc, fix, rec := accesscoretest.BuildIdentityManageService(t)

	// Seed admin user so last-admin protection does not block the Create call.
	adminUser, err := domain.NewUser("admin", "admin@test.com", "$2a$04$dummy", clock.Real().Now())
	require.NoError(t, err)
	adminUser.ID = "usr-admin"
	require.NoError(t, fix.SeedUser(ctx, adminUser))
	adminRole := &domain.Role{ID: "admin", Name: "Admin"}
	require.NoError(t, fix.SeedRole(ctx, adminRole))
	require.NoError(t, fix.SeedAssignment(ctx, adminUser.ID, "admin"))

	// Use admin context (actorFromContext requires a non-empty subject).
	adminCtx := auth.TestContext("usr-admin", []string{"admin"})

	created, err := svc.Create(adminCtx, identitymanage.CreateInput{
		Username: "newuser",
		Email:    "new@test.com",
		Password: "pass1234",
	})
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)

	// Fixture state: new user must be visible via UserRepo.
	got, err := fix.UserRepo().GetByID(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "newuser", got.Username)

	// Recorder: a user-created event must have been emitted.
	entries := rec.EntriesByType(identitymanage.TopicUserCreated)
	require.Len(t, entries, 1, "Create must emit exactly one user-created event")
}

// TestBuildConfigReceiveServiceSmoke builds the configreceive service, exercises
// HandleEntryUpserted with a stubbed ConfigGetter, and asserts the stub was queried.
func TestBuildConfigReceiveServiceSmoke(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	fg := accesscoretest.NewFakeConfigGetter(map[string]accesscoretest.ConfigGetterStub{
		"jwt.ttl": {Entry: &accesscoretest.FakeConfigEntry{Key: "jwt.ttl", Value: "3600", Version: 1}},
	})
	svc := accesscoretest.BuildConfigReceiveService(t,
		accesscoretest.WithReceiveConfigGetter(fg),
	)

	// Build a minimal outbox.Entry carrying a config-upserted payload.
	entry := makeConfigUpsertedEntry("jwt.ttl", 1)
	result := svc.HandleEntryUpserted(ctx, entry)
	// Ack is the expected result when stub returns an entry.
	_ = result

	calls := fg.Calls()
	require.Contains(t, calls, "jwt.ttl", "HandleEntryUpserted must query ConfigGetter for the upserted key")
}

// makeConfigUpsertedEntry builds a minimal outbox.Entry with a valid
// event.config.entry-upserted.v1 payload for the given key and version.
func makeConfigUpsertedEntry(key string, version int) outbox.Entry {
	payload, _ := json.Marshal(map[string]any{
		"key":     key,
		"version": version,
		"actorId": "test-actor",
	})
	return outbox.Entry{
		ID:        "entry-" + key,
		EventType: "event.config.entry-upserted.v1",
		Payload:   payload,
	}
}
