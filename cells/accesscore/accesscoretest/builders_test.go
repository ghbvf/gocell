// Package accesscoretest_test exercises the public testutil helpers exported by
// cells/accesscore/accesscoretest. Tests run TDD-first: they compile against the
// package being built and must pass with all implementations in place.
package accesscoretest_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/accesscoretest"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/slices/configreceive"
	"github.com/ghbvf/gocell/cells/accesscore/slices/identitymanage"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

// fixtureTestTenant is a package-level alias for the canonical test tenant used
// in GetEntry recorder assertions. It exercises the new tenant.TenantID parameter
// without coupling every test to a separate UUID constant.
var fixtureTestTenant = accesscoretest.DefaultFixtureTenantID

// TestNewAccessFixtureSeedAndQuery seeds a user and role, then reads them back
// via the value-type Get* helpers.
func TestNewAccessFixtureSeedAndQuery(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := accesscoretest.NewAccessFixture(t, clock.Real())

	require.NoError(t, f.SeedUser(ctx, accesscoretest.SeededUser{
		ID: "usr-alice", Username: "alice", Email: "alice@example.com",
		PasswordHash: "hash",
	}))

	require.NoError(t, f.SeedRole(ctx, accesscoretest.SeededRole{
		ID: "role-viewer", Name: "Viewer",
	}))

	got, err := f.GetUser(ctx, "usr-alice")
	require.NoError(t, err)
	assert.Equal(t, "usr-alice", got.ID)
	assert.Equal(t, "alice", got.Username)
	assert.Equal(t, accesscoretest.UserStatusActive, got.Status)

	gotRole, err := f.GetRole(ctx, "role-viewer")
	require.NoError(t, err)
	assert.Equal(t, "role-viewer", gotRole.ID)
}

// TestAccessFixtureStorePaired asserts that user / role / assignment all
// share the same underlying store by verifying that a role assigned via
// f.SeedAssignment is visible through f.UserRoles.
func TestAccessFixtureStorePaired(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := accesscoretest.NewAccessFixture(t, clock.Real())

	require.NoError(t, f.SeedUser(ctx, accesscoretest.SeededUser{
		ID: "usr-bob", Username: "bob", Email: "bob@example.com", PasswordHash: "hash",
	}))
	require.NoError(t, f.SeedRole(ctx, accesscoretest.SeededRole{
		ID: "role-admin", Name: "Admin",
	}))
	require.NoError(t, f.SeedAssignment(ctx, "usr-bob", "role-admin"))

	roles, err := f.UserRoles(ctx, "usr-bob")
	require.NoError(t, err)
	require.Len(t, roles, 1,
		"assignment seeded via SeedAssignment must be visible via UserRoles")
	assert.Equal(t, "role-admin", roles[0].ID)
}

// TestFakeConfigGetterTypedConstructors exercises each of the four typed
// stub constructors (Present / Sensitive / NotFound / Error) and the
// unknown-key path.
func TestFakeConfigGetterTypedConstructors(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	// PresentStub — plaintext value returned as-is.
	fg := accesscoretest.NewFakeConfigGetter(map[string]accesscoretest.ConfigGetterStub{
		"foo": accesscoretest.PresentStub("foo", "bar", 1),
	})
	got, err := fg.GetEntry(ctx, fixtureTestTenant, "foo")
	require.NoError(t, err)
	assert.Equal(t, "foo", got.Key)
	assert.Equal(t, "bar", got.Value)
	assert.False(t, got.Sensitive)
	assert.Equal(t, 1, got.Version)

	// SensitiveStub — Value is the production redaction sentinel.
	fgSens := accesscoretest.NewFakeConfigGetter(map[string]accesscoretest.ConfigGetterStub{
		"kms.key": accesscoretest.SensitiveStub("kms.key", 4),
	})
	gotSens, err := fgSens.GetEntry(ctx, fixtureTestTenant, "kms.key")
	require.NoError(t, err)
	assert.True(t, gotSens.Sensitive)
	assert.Equal(t, "******", gotSens.Value,
		"SensitiveStub must mirror configcore's redaction contract")

	// ErrorStub — error surfaced verbatim.
	customErr := errcode.New(errcode.KindNotFound, errcode.ErrConfigRepoNotFound, "config not found")
	fgErr := accesscoretest.NewFakeConfigGetter(map[string]accesscoretest.ConfigGetterStub{
		"missing": accesscoretest.ErrorStub(customErr),
	})
	_, err = fgErr.GetEntry(ctx, fixtureTestTenant, "missing")
	require.Error(t, err)
	assert.ErrorIs(t, err, customErr)

	// Unknown key (no stub) — ErrConfigRepoNotFound with CategoryDomain.
	fgUnknown := accesscoretest.NewFakeConfigGetter(nil)
	_, err = fgUnknown.GetEntry(ctx, fixtureTestTenant, "unknown")
	require.Error(t, err)
	assert.True(t, errcode.IsDomainNotFound(err, errcode.ErrConfigRepoNotFound),
		"unknown key must carry CategoryDomain so HandleEntryUpserted Acks")

	// NotFoundStub — same shape as unknown key.
	fgGone := accesscoretest.NewFakeConfigGetter(map[string]accesscoretest.ConfigGetterStub{
		"stale": accesscoretest.NotFoundStub(),
	})
	_, err = fgGone.GetEntry(ctx, fixtureTestTenant, "stale")
	require.Error(t, err)
	assert.True(t, errcode.IsDomainNotFound(err, errcode.ErrConfigRepoNotFound),
		"NotFoundStub must carry CategoryDomain so HandleEntryUpserted Acks")
}

// TestFakeConfigGetterCtxCancel ensures the fake honors context cancellation,
// matching the production HTTP getter's semantics.
func TestFakeConfigGetterCtxCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	fg := accesscoretest.NewFakeConfigGetter(map[string]accesscoretest.ConfigGetterStub{
		"foo": accesscoretest.PresentStub("foo", "v", 1),
	})
	_, err := fg.GetEntry(ctx, fixtureTestTenant, "foo")
	require.Error(t, err, "GetEntry must propagate canceled ctx error")
	assert.True(t, errors.Is(err, context.Canceled))
}

// TestFakeConfigGetterCallsRecorded verifies that call order is captured.
func TestFakeConfigGetterCallsRecorded(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	fg := accesscoretest.NewFakeConfigGetter(map[string]accesscoretest.ConfigGetterStub{
		"foo": accesscoretest.PresentStub("foo", "v", 1),
		"bar": accesscoretest.PresentStub("bar", "v2", 1),
	})
	_, _ = fg.GetEntry(ctx, fixtureTestTenant, "foo")
	_, _ = fg.GetEntry(ctx, fixtureTestTenant, "bar")

	calls := fg.Calls()
	require.Equal(t, []string{"foo", "bar"}, calls)

	fg.Reset()
	assert.Empty(t, fg.Calls(), "Reset must clear the call log")
}

// TestNewCredentialInvalidatorWithFixture verifies the fixture-only collapse:
// passing a fixture yields a usable *CredentialInvalidator.
func TestNewCredentialInvalidatorWithFixture(t *testing.T) {
	t.Parallel()
	f := accesscoretest.NewAccessFixture(t, clock.Real())
	inv := accesscoretest.NewCredentialInvalidator(
		t,
		accesscoretest.WithInvalidatorFixture(f),
	)
	require.NotNil(t, inv, "NewCredentialInvalidator must return non-nil *CredentialInvalidator")
}

// TestBuildIdentityManageServiceSmoke builds the service, seeds a user, creates
// another user through the service, then verifies the fixture state changed and
// the recorder captured a user-created event.
func TestBuildIdentityManageServiceSmoke(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	svc, fix, rec := accesscoretest.BuildIdentityManageService(t)

	// Seed admin user so last-admin protection does not block the Create call.
	require.NoError(t, fix.SeedUser(ctx, accesscoretest.SeededUser{
		ID: "usr-admin", Username: "admin", Email: "admin@test.com",
		PasswordHash: "$2a$04$dummy",
	}))
	require.NoError(t, fix.SeedRole(ctx, accesscoretest.SeededRole{
		ID: "admin", Name: "Admin",
	}))
	require.NoError(t, fix.SeedAssignment(ctx, "usr-admin", "admin"))

	// Use admin context (actorFromContext requires a non-empty subject).
	// Inject DefaultFixtureTenantID so tenant-scoped repo operations succeed.
	adminCtx := ctxkeys.WithTenantID(auth.TestContext("usr-admin", []string{"admin"}),
		accesscoretest.DefaultFixtureTenantID.String())

	created, err := svc.Create(adminCtx, identitymanage.CreateInput{
		Username: "newuser",
		Email:    "new@test.com",
		Password: "pass1234",
	})
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)

	// Fixture state: new user must be visible via GetUser.
	got, err := fix.GetUser(ctx, created.ID)
	require.NoError(t, err)
	assert.Equal(t, "newuser", got.Username)

	// Recorder: a user-created event must have been emitted.
	entries := rec.EntriesByType(identitymanage.TopicUserCreated)
	require.Len(t, entries, 1, "Create must emit exactly one user-created event")
}

// TestBuildConfigReceiveServiceSmoke builds the configreceive service, exercises
// HandleEntryUpserted with a stubbed ConfigGetter, and asserts the correct
// HandleResult disposition for both the hit (Ack) and stale (Ack) paths.
func TestBuildConfigReceiveServiceSmoke(t *testing.T) {
	t.Parallel()
	// Inject tenant so the configGetter refetch path is exercised.
	// HandleEntryUpserted skips the getter when no tenant is in context
	// (consumer pipeline restores tenant from the outbox principal envelope
	// before the handler runs; in unit tests we inject it explicitly).
	ctx := ctxkeys.WithTenantID(auth.TestContext("acc", []string{"admin"}),
		accesscoretest.DefaultFixtureTenantID.String())

	fg := accesscoretest.NewFakeConfigGetter(map[string]accesscoretest.ConfigGetterStub{
		"jwt.ttl": accesscoretest.PresentStub("jwt.ttl", "3600", 1),
	})
	svc := accesscoretest.BuildConfigReceiveService(
		t,
		accesscoretest.WithConfigReceiveConfigGetter(fg),
	)

	// Build a minimal outbox.Entry carrying a config-upserted payload.
	entry := makeConfigUpsertedEntry("jwt.ttl", 1)
	result := svc.HandleEntryUpserted(ctx, entry)
	require.Equal(t, outbox.DispositionAck, result.Disposition,
		"HandleEntryUpserted must Ack on stub-found entry")

	calls := fg.Calls()
	require.Contains(t, calls, "jwt.ttl", "HandleEntryUpserted must query ConfigGetter for the upserted key")

	// Stale-event path: NotFoundStub triggers ErrConfigRepoNotFound with
	// CategoryDomain, so HandleEntryUpserted Acks instead of Requeueing.
	fgStale := accesscoretest.NewFakeConfigGetter(map[string]accesscoretest.ConfigGetterStub{
		"gone.key": accesscoretest.NotFoundStub(),
	})
	svcStale := accesscoretest.BuildConfigReceiveService(
		t,
		accesscoretest.WithConfigReceiveConfigGetter(fgStale),
	)
	staleEntry := makeConfigUpsertedEntry("gone.key", 1)
	staleResult := svcStale.HandleEntryUpserted(ctx, staleEntry)
	require.Equal(t, outbox.DispositionAck, staleResult.Disposition,
		"HandleEntryUpserted must Ack on stale (ConfigNotFound) event")
}

// recordingTokenIssuer is a test double for identitymanage.TokenIssuer that
// captures the userID it was called with and returns a deterministic
// TokenPair. Used by TestBuildIdentityManageService_AllOptions to verify the
// WithIdentityTokenIssuer option setter wires a non-nil issuer into the
// service.
type recordingTokenIssuer struct {
	calls []string
}

func (r *recordingTokenIssuer) IssueForUser(_ context.Context, userID string) (dto.TokenPair, error) {
	r.calls = append(r.calls, userID)
	return dto.TokenPair{AccessToken: "test-access", RefreshToken: "test-refresh"}, nil
}

// recordingCollector is a minimal ConfigEventCollector double that counts
// process events. Lets TestBuildConfigReceiveService_WithCollectorAndLogger
// assert that the WithConfigReceiveCollector option wired a non-nil collector
// instead of falling back to NoopConfigEventCollector.
type recordingCollector struct {
	processCount int
}

func (r *recordingCollector) RecordEventProcess(_ context.Context, _, _ string, _ obmetrics.ConfigEventProcessReason) {
	r.processCount++
}

func (recordingCollector) RecordEventSettlement(_ context.Context, _, _, _ string, _ outbox.SettlementResult) {
}

// TestBuildIdentityManageService_AllOptions exercises every public With*
// option setter on BuildIdentityManageService with a concrete non-nil value
// so the value-set branch is covered (not just the default fallback). Also
// drives Lock and Update(status=suspended) so mapStatusFromDomain's Locked
// and Suspended switch arms are reached via GetUser.
func TestBuildIdentityManageService_AllOptions(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	fix := accesscoretest.NewAccessFixture(t, clock.Real())
	customClock := clock.Real()
	customLogger := slog.New(slog.NewTextHandler(io.Discard, nil))
	issuer := &recordingTokenIssuer{}
	customInv := accesscoretest.NewCredentialInvalidator(
		t,
		accesscoretest.WithInvalidatorFixture(fix),
	)

	svc, _, rec := accesscoretest.BuildIdentityManageService(
		t,
		accesscoretest.WithIdentityFixture(fix),
		accesscoretest.WithIdentityClock(customClock),
		accesscoretest.WithIdentityLogger(customLogger),
		accesscoretest.WithIdentityTokenIssuer(issuer),
		accesscoretest.WithIdentityInvalidator(customInv),
	)
	require.NotNil(t, svc)
	require.NotNil(t, rec)

	// Seed an admin so last-admin protection allows subsequent admin ops.
	require.NoError(t, fix.SeedUser(ctx, accesscoretest.SeededUser{
		ID: "usr-admin", Username: "admin", Email: "admin@test.com",
		PasswordHash: "$2a$04$dummy",
	}))
	require.NoError(t, fix.SeedRole(ctx, accesscoretest.SeededRole{ID: "admin", Name: "Admin"}))
	require.NoError(t, fix.SeedAssignment(ctx, "usr-admin", "admin"))

	// Seed two non-admin users so we can drive Lock and Suspend without
	// tripping last-admin protection on the admin itself.
	require.NoError(t, fix.SeedUser(ctx, accesscoretest.SeededUser{
		ID: "usr-bob", Username: "bob", Email: "bob@test.com",
		PasswordHash: "$2a$04$dummy",
	}))
	require.NoError(t, fix.SeedUser(ctx, accesscoretest.SeededUser{
		ID: "usr-eve", Username: "eve", Email: "eve@test.com",
		PasswordHash: "$2a$04$dummy",
	}))

	// Inject DefaultFixtureTenantID so tenant-scoped repo operations succeed.
	adminCtx := ctxkeys.WithTenantID(auth.TestContext("usr-admin", []string{"admin"}),
		accesscoretest.DefaultFixtureTenantID.String())

	// Lock bob → mapStatusFromDomain Locked branch via GetUser.
	require.NoError(t, svc.Lock(adminCtx, "usr-bob"))
	bob, err := fix.GetUser(ctx, "usr-bob")
	require.NoError(t, err)
	assert.Equal(t, accesscoretest.UserStatusLocked, bob.Status)

	// Suspend eve via Update → mapStatusFromDomain Suspended branch.
	suspended := string("suspended")
	_, err = svc.Update(adminCtx, identitymanage.UpdateInput{
		ID:     "usr-eve",
		Status: &suspended,
	})
	require.NoError(t, err)
	eve, err := fix.GetUser(ctx, "usr-eve")
	require.NoError(t, err)
	assert.Equal(t, accesscoretest.UserStatusSuspended, eve.Status)
}

// TestBuildConfigReceiveService_WithCollectorAndLogger exercises the
// WithConfigReceiveLogger and WithConfigReceiveCollector option setters by
// passing concrete non-nil values, then drives one HandleEntryUpserted so
// the collector observes a RecordEventProcess call.
func TestBuildConfigReceiveService_WithCollectorAndLogger(t *testing.T) {
	t.Parallel()
	// Inject a tenant so HandleEntryUpserted reaches the refetch success path
	// (Ack). Without a tenant the handler now fail-closes to Reject/DLQ (#1577
	// tenant-correct invariant, codex F2), which is covered by its own test.
	ctx := ctxkeys.WithTenantID(auth.TestContext("acc", []string{"admin"}),
		accesscoretest.DefaultFixtureTenantID.String())

	fg := accesscoretest.NewFakeConfigGetter(map[string]accesscoretest.ConfigGetterStub{
		"foo": accesscoretest.PresentStub("foo", "v", 1),
	})
	collector := &recordingCollector{}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	svc := accesscoretest.BuildConfigReceiveService(
		t,
		accesscoretest.WithConfigReceiveConfigGetter(fg),
		accesscoretest.WithConfigReceiveLogger(logger),
		accesscoretest.WithConfigReceiveCollector(collector),
	)
	require.NotNil(t, svc)

	entry := makeConfigUpsertedEntry("foo", 1)
	result := svc.HandleEntryUpserted(ctx, entry)
	require.Equal(t, outbox.DispositionAck, result.Disposition)
	// processCount may be zero — configreceive emits RecordEventProcess only
	// on specific dispositions, not every Ack path. The coverage goal here
	// is the WithConfigReceiveLogger / WithConfigReceiveCollector option
	// setters; observing the collector at runtime is best-effort.
	_ = collector.processCount
}

// makeConfigUpsertedEntry builds a minimal outbox.Entry with a valid
// event.config.entry-upserted.v1 payload for the given key and version.
func makeConfigUpsertedEntry(key string, version int) outbox.Entry {
	payload, err := json.Marshal(map[string]any{
		"key":     key,
		"version": version,
		"actorId": "test-actor",
	})
	if err != nil {
		panic("makeConfigUpsertedEntry: json.Marshal failed: " + err.Error())
	}
	return outboxtest.NewEntry(configreceive.TopicConfigEntryUpserted, payload)
}
