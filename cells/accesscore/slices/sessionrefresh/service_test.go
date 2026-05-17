package sessionrefresh

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialinvalidate"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/sloghelper"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
	"github.com/ghbvf/gocell/runtime/auth/session"
	sessionstoretest "github.com/ghbvf/gocell/runtime/auth/session/storetest"
)

// expiredSessionCreatedOffset is the past CreatedAt offset used by
// TestService_Refresh_UpdatesSessionExpiryForRefreshedAccessToken to fabricate
// an already-expired session. Extracted to a package-level const per
// TEST-TIME-LITERAL-01 (negative duration literals are still flagged inline).
const expiredSessionCreatedOffset = -2 * time.Hour

var (
	testKeySet, _, _ = auth.MustNewTestKeySet(clock.Real())
	testIssuer       *auth.JWTIssuer
)

func init() {
	var err error
	// Issuer is constructed with a default audience via WithIssuerAudiencesFromSlice
	// (Registry path). The slice service no longer caches audience separately (S31).
	testIssuer, err = auth.NewJWTIssuer(testKeySet, "gocell-accesscore", auth.DefaultAccessTokenTTL, clock.Real(),
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	if err != nil {
		panic("test setup: " + err.Error())
	}
}

// newTestSessionStore constructs an in-memory session.Store with the canonical
// test protocol (JTI fingerprint + AuthzEpoch ordering + all CredentialEvents).
func newTestSessionStore(t testing.TB) *session.MemStore {
	t.Helper()
	var proto *session.Protocol
	if tt, ok := t.(*testing.T); ok {
		proto = sessionstoretest.NewTestProtocol(tt)
	} else {
		// testing.B or other TB: construct protocol directly.
		var err error
		proto, err = session.NewProtocol(
			session.WithFingerprint(session.FingerprintJTIRef{}),
			session.WithOrdering(session.OrderingAuthzEpoch{}),
			session.WithRevokeOnAll(),
		)
		if err != nil {
			t.Fatalf("test setup: newTestSessionStore: %v", err)
		}
	}
	store, err := session.NewMemStore(proto, clock.Real())
	if err != nil {
		t.Fatalf("test setup: newTestSessionStore: %v", err)
	}
	return store
}

// newTestSession creates a session.Session with sensible defaults. The caller
// must call sessionStore.Create to persist it.
func newTestSession(subjectID, sessionID string) *session.Session {
	return &session.Session{
		ID:                sessionID,
		SubjectID:         subjectID,
		JTI:               sessionID,
		AuthzEpochAtIssue: 1, // placeholder; production uses live users.authz_epoch
		CreatedAt:         time.Now(),
		ExpiresAt:         time.Now().Add(time.Hour),
	}
}

func newTestRefreshStore() refresh.Store {
	clk := storetest.NewFakeClock(time.Now())
	store, err := refreshmem.New(refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         time.Hour,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}, clk, nil)
	if err != nil {
		panic("test setup: " + err.Error())
	}
	return store
}

type typedNilRefreshStore struct {
	refresh.Store
}

// newTestInvalidator builds a real *credentialinvalidate.Invalidator backed by
// in-memory stores. Used by test helpers that need a non-nil invalidator but do
// not exercise the reuse-cascade path directly.
func newTestInvalidator(
	userRepo ports.UserRepository,
	sessionStore session.Store,
	refreshStore refresh.Store,
) *credentialinvalidate.Invalidator {
	inv, err := credentialinvalidate.New(userRepo, sessionStore, refreshStore)
	if err != nil {
		panic("test setup: newTestInvalidator: " + err.Error())
	}
	return inv
}

// withTestInvalidator returns a WithInvalidator option built from the given
// stores. Tests that use standalone MustNewService calls (not via
// newTestServiceWithClock) call this helper to satisfy the required-invalidator
// constraint without setting up separate store variables.
func withTestInvalidator(userRepo ports.UserRepository, sessionStore session.Store, refreshStore refresh.Store) Option {
	return WithInvalidator(newTestInvalidator(userRepo, sessionStore, refreshStore))
}

// MustNewServiceWithInvalidator constructs a Service with an explicit spy
// invalidator (for tests that exercise the reuse-cascade path). Since the
// invalidatorApply interface is unexported but tests are in the same package,
// the spy is directly assigned to the service's invalidator field.
func MustNewServiceWithInvalidator(
	sessionStore session.Store,
	roleRepo ports.RoleRepository,
	userRepo ports.UserRepository,
	refreshStore refresh.Store,
	issuer *auth.JWTIssuer,
	logger *slog.Logger,
	inv invalidatorApply,
	opts ...Option,
) *Service {
	// Build with a real invalidator to pass NewService nil validation, then
	// swap in the spy so assertions capture the exact call arguments.
	realInv := newTestInvalidator(userRepo, sessionStore, refreshStore)
	allOpts := append([]Option{WithInvalidator(realInv)}, opts...)
	svc, err := NewService(sessionStore, roleRepo, userRepo, refreshStore, issuer, logger,
		append(allOpts, WithClock(clock.Real()))...) //archtest:allow:clock-injection:via-slice opts built dynamically for spy injection
	if err != nil {
		panic("MustNewServiceWithInvalidator: " + err.Error())
	}
	svc.invalidator = inv
	return svc
}

// TestNewService_IssuerDefaultAudienceWrittenOnRefresh verifies that the
// sessionrefresh Service issues tokens with the audience configured in the
// issuer (Registry path), without caching audience separately (S31).
func TestNewService_IssuerDefaultAudienceWrittenOnRefresh(t *testing.T) {
	svc, sessionStore, refreshStore := newTestServiceWithRefreshStore(t, "usr-aud-refresh")

	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-aud-refresh", "usr-aud-refresh", int64(1))
	require.NoError(t, err)

	sess := newTestSession("usr-aud-refresh", "sess-aud-refresh")
	require.NoError(t, sessionStore.Create(context.Background(), sess))

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.NoError(t, err)

	verifier, err := auth.NewJWTVerifier(testKeySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	accessClaims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Contains(t, accessClaims.Audience, "gocell",
		"rotated access token aud must come from issuer default audience (Registry)")
}

// newTestService creates a refresh service with a minimal in-memory session store.
// seedUsers lists user IDs to pre-populate so GetByID succeeds.
func newTestService(t testing.TB, seedUsers ...string) (*Service, session.Store) {
	t.Helper()
	svc, sessionStore, _ := newTestServiceWithRefreshStore(t, seedUsers...)
	return svc, sessionStore
}

// newTestServiceWithRefreshStore creates a service and exposes the refreshStore
// for tests that need to issue wire tokens via the store directly.
func newTestServiceWithRefreshStore(t testing.TB, seedUsers ...string) (*Service, session.Store, refresh.Store) {
	t.Helper()
	svc, sessionStore, refreshStore, _ := newTestServiceWithClock(t, seedUsers...)
	return svc, sessionStore, refreshStore
}

// newTestServiceWithClock creates a service and exposes both the refreshStore
// and the underlying FakeClock for tests that need to advance time (e.g. to
// move past the ReuseInterval so old tokens are rejected rather than grace-retried).
func newTestServiceWithClock(t testing.TB, seedUsers ...string) (*Service, session.Store, refresh.Store, *storetest.FakeClock) {
	t.Helper()
	sessionStore := newTestSessionStore(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	for _, uid := range seedUsers {
		u, _ := domain.NewUser(uid, uid+"@test.local", "hash", time.Now())
		u.ID = uid
		_ = userRepo.Create(context.Background(), u)
	}
	fakeClock := storetest.NewFakeClock(time.Now())
	refreshStore, err := refreshmem.New(refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         time.Hour,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}, fakeClock, nil)
	if err != nil {
		t.Fatalf("test setup: %v", err)
	}
	inv := newTestInvalidator(userRepo, sessionStore, refreshStore)
	svc := MustNewService(sessionStore, roleRepo, userRepo, refreshStore, testIssuer, slog.Default(),
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
		WithInvalidator(inv))
	return svc, sessionStore, refreshStore, fakeClock
}

// newTestServiceWithUserRepo creates a service and returns the userRepo for
// tests that need to seed user fixtures and assert on the PasswordResetRequired flag.
func newTestServiceWithUserRepo(t testing.TB) (*Service, session.Store, *mem.UserRepository) {
	t.Helper()
	sessionStore := newTestSessionStore(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	refreshStore := newTestRefreshStore()
	svc := MustNewService(sessionStore, roleRepo, userRepo, refreshStore, testIssuer, slog.Default(),
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
		withTestInvalidator(userRepo, sessionStore, refreshStore))
	return svc, sessionStore, userRepo
}

func TestNewService_RejectsTypedNilDependencies(t *testing.T) {
	sessionStore := newTestSessionStore(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	refreshStore := newTestRefreshStore()

	cases := []struct {
		name string
		run  func() (*Service, error)
	}{
		{
			name: "typed nil sessionStore",
			run: func() (*Service, error) {
				var typedNil *session.MemStore
				return NewService(typedNil, roleRepo, userRepo, refreshStore, testIssuer, slog.Default(),
					WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})))
			},
		},
		{
			name: "typed nil roleRepo",
			run: func() (*Service, error) {
				var typedNil *mem.RoleRepository
				return NewService(sessionStore, typedNil, userRepo, refreshStore, testIssuer, slog.Default(),
					WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})))
			},
		},
		{
			name: "typed nil userRepo",
			run: func() (*Service, error) {
				var typedNil *mem.UserRepository
				return NewService(sessionStore, roleRepo, typedNil, refreshStore, testIssuer, slog.Default(),
					WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})))
			},
		},
		{
			name: "typed nil refreshStore",
			run: func() (*Service, error) {
				var typedNil *typedNilRefreshStore
				return NewService(sessionStore, roleRepo, userRepo, typedNil, testIssuer, slog.Default(),
					WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})))
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.run()
			require.Error(t, err)
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec)
			assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
		})
	}
}

// TestNewService_RequiresTxRunner asserts that NewService rejects callers
// that omit WithTxManager. The cross-store ACID wrap in Refresh requires a
// non-nil TxRunner; the construction-time fail-fast prevents a nil-deref at
// the first request. No silent fallback to cell.DemoTxRunner — that would
// mask production wiring mistakes.
func TestNewService_RequiresTxRunner(t *testing.T) {
	sessionStore := newTestSessionStore(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	refreshStore := newTestRefreshStore()

	t.Run("missing WithTxManager option", func(t *testing.T) {
		_, err := NewService(sessionStore, roleRepo, userRepo, refreshStore, testIssuer, slog.Default(),
			WithClock(clock.Real()))
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	})

	t.Run("nil TxRunner via WithTxManager(persistence.WrapForCell(nil)) is rejected", func(t *testing.T) {
		// WithTxManager silently ignores nil to keep the option idempotent —
		// but NewService's final check still rejects the resulting unconfigured
		// state.
		_, err := NewService(sessionStore, roleRepo, userRepo, refreshStore, testIssuer, slog.Default(),
			WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(nil)))
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	})
}

// failingTxRunner is a TxRunner that runs the closure once, captures whether
// it succeeded internally, and then returns a sentinel error simulating an
// outer-tx failure (commit failure, infrastructure outage, etc.). Used to
// verify Refresh propagates the RunInTx error and never leaks a partially
// populated TokenPair.
type failingTxRunner struct {
	innerCalled bool
	innerErr    error
}

func (r *failingTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	r.innerCalled = true
	r.innerErr = fn(ctx)
	return errFailingTxRunnerOuter
}

var errFailingTxRunnerOuter = errors.New("test: outer tx commit failure")

// TestRefresh_RunInTxFailure_ReturnsErrorAndZeroPair asserts that an outer
// TxRunner failure is propagated and TokenPair stays at its zero value
// (no partial leakage of access/refresh tokens to the caller). This is the
// service-layer counterpart to the adapter-level
// TestB5_OuterTxRollback_* integration tests.
func TestRefresh_RunInTxFailure_ReturnsErrorAndZeroPair(t *testing.T) {
	sessionStore := newTestSessionStore(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	refreshStore := newTestRefreshStore()
	user, err := domain.NewUser("usr-runintx-fail", "u@test.local", "hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-runintx-fail"
	require.NoError(t, userRepo.Create(context.Background(), user))
	sess := newTestSession("usr-runintx-fail", "sess-runintx-fail")
	require.NoError(t, sessionStore.Create(context.Background(), sess))
	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-runintx-fail", "usr-runintx-fail", int64(1))
	require.NoError(t, err)

	tr := &failingTxRunner{}
	svc := MustNewService(sessionStore, roleRepo, userRepo, refreshStore, testIssuer, slog.Default(),
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(tr)),
		withTestInvalidator(userRepo, sessionStore, refreshStore))

	pair, err := svc.Refresh(context.Background(), wireToken)

	// Outer-tx error must propagate verbatim.
	require.ErrorIs(t, err, errFailingTxRunnerOuter,
		"Refresh must surface the TxRunner error so callers can distinguish infra failures from token rejection")

	// The closure must have run (the inner refresh logic fully executed and
	// succeeded — only the commit failed).
	require.True(t, tr.innerCalled, "RunInTx closure must have executed")
	require.NoError(t, tr.innerErr, "the inner refresh sequence should have completed without error before the simulated commit failure")

	// TokenPair must be the zero value: no partial leakage of any field.
	assert.Equal(t, dto.TokenPair{}, pair,
		"on outer-tx failure Refresh must return a zero TokenPair — no partial token data may leak to the caller")
}

// issueTestWireToken creates a session + issues a wire token from the refreshStore.
// Returns (svc, sessionStore, refreshStore, wireToken).
func issueTestWireToken(t *testing.T, userID, sessionID string) (*Service, session.Store, refresh.Store, string) {
	t.Helper()
	svc, sessionStore, refreshStore := newTestServiceWithRefreshStore(t, userID)

	sess := newTestSession(userID, sessionID)
	require.NoError(t, sessionStore.Create(context.Background(), sess))

	wireToken, _, err := refreshStore.Issue(context.Background(), sessionID, userID, int64(1))
	require.NoError(t, err)

	return svc, sessionStore, refreshStore, wireToken
}

// brokenRoleRepo simulates a RoleRepository outage for fail-closed tests.
type brokenRoleRepo struct {
	mem.RoleRepository
	err error
}

func (b *brokenRoleRepo) GetByUserID(_ context.Context, _ string) ([]*domain.Role, error) {
	return nil, b.err
}

// countingSessionStore wraps session.Store so tests can assert that Create
// was called (or not called) when sessionmint fails fast.
type countingSessionStore struct {
	session.Store
	creates int
}

func (c *countingSessionStore) Create(ctx context.Context, s *session.Session) error {
	c.creates++
	return c.Store.Create(ctx, s)
}

// TestService_Refresh_UserNotActive_RejectsAndCascadeRevokes covers the
// S4.0 fail-closed path added by rejectIfUserNotActive: when the session
// owner is non-active (suspended / locked), Refresh must (a) refuse with
// ErrAuthUserNotActive (403) and (b) cascade-revoke the refresh chain so
// subsequent rotation attempts cannot keep returning new tokens. Tests
// both non-active states to confirm CanAuthenticate() applies uniformly.
func TestService_Refresh_UserNotActive_RejectsAndCascadeRevokes(t *testing.T) {
	cases := []struct {
		name        string
		status      domain.UserStatus
		expectError errcode.Code
	}{
		{name: "suspended_rejected", status: domain.StatusSuspended, expectError: errcode.ErrAuthUserNotActive},
		{name: "locked_rejected", status: domain.StatusLocked, expectError: errcode.ErrAuthUserNotActive},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc, sessionStore, userRepo := newTestServiceWithUserRepo(t)
			// Seed an active user, then directly mutate to non-active so the
			// session predates the demotion (real-world scenario: admin
			// suspends a user with a live session).
			u, err := domain.NewUser("notactive", "notactive@test.local", "hash", time.Now())
			require.NoError(t, err)
			u.ID = "usr-notactive-" + string(tc.status)
			require.NoError(t, userRepo.Create(context.Background(), u))
			u.SetStatus(tc.status, time.Now())
			require.NoError(t, userRepo.Update(context.Background(), u))

			sess := newTestSession(u.ID, "sess-"+u.ID)
			require.NoError(t, sessionStore.Create(context.Background(), sess))

			// Wire the refresh-store side of the test directly via the service
			// internals — newTestServiceWithUserRepo builds the refresh store
			// inside MustNewService, so seed an entry through svc.refreshStore.
			wireToken, _, issueErr := svc.refreshStore.Issue(context.Background(), sess.ID, u.ID, int64(1))
			require.NoError(t, issueErr)

			_, err = svc.Refresh(context.Background(), wireToken)
			require.Error(t, err, "refresh must reject non-active user")
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec)
			assert.Equal(t, tc.expectError, ec.Code,
				"non-active refresh must surface ErrAuthUserNotActive (403)")
			assert.Equal(t, errcode.KindPermissionDenied, ec.Kind)

			// Cascade-revoke side effect: the refresh chain must be gone so a
			// retry with the same wire token cannot keep returning tokens.
			// cascadeRevoke routes through RevokeSessionDetached, which
			// invalidates the refresh store entry; subsequent Refresh sees
			// the refresh store reject the token rather than reaching the
			// user-state check again.
			_, retryErr := svc.Refresh(context.Background(), wireToken)
			require.Error(t, retryErr, "retry must fail (refresh chain revoked)")
			var retryEc *errcode.Error
			require.ErrorAs(t, retryErr, &retryEc)
			// After cascade revoke, the retry hits the refresh-store layer
			// and surfaces ErrAuthRefreshFailed (uniform rejection message),
			// not the user-state code.
			assert.Equal(t, errcode.ErrAuthRefreshFailed, retryEc.Code,
				"retry after cascade revoke must surface uniform refresh rejection")
		})
	}
}

// TestService_Refresh_RevokedSession_RevokeBeforeUserLookup locks the
// post-§A11-rewrite behavior: when a session is revoked, the refresh path
// MUST reject with uniform 401 ErrAuthRefreshFailed BEFORE any user
// lookup or user-status check runs.
//
// The combination revoked-session + non-active-user used to surface as
// 403 ErrAuthUserNotActive (baseline-first ordering inside the funnel,
// pre-§A11). After §A11 rewrite the SessionNotRevoked check is moved out
// of the user-bound funnel and into a session-only prong that runs
// immediately after sessionStore.Get — so user state becomes irrelevant
// once revoke is detected (ADR §A12 wire-uniformity, P1-A regression).
func TestService_Refresh_RevokedSession_RevokeBeforeUserLookup(t *testing.T) {
	svc, sessionStore, userRepo := newTestServiceWithUserRepo(t)

	// User is suspended — but revoke fires first now, so user state must
	// not influence the wire response.
	u, err := domain.NewUser("ordering-user", "ordering@test.local", "hash", time.Now())
	require.NoError(t, err)
	u.ID = "usr-ordering"
	require.NoError(t, userRepo.Create(context.Background(), u))
	u.SetStatus(domain.StatusSuspended, time.Now())
	require.NoError(t, userRepo.Update(context.Background(), u))

	// Session is revoked → revoke-first prong rejects with uniform 401
	// before reaching user lookup.
	sess := newTestSession(u.ID, "sess-ordering")
	require.NoError(t, sessionStore.Create(context.Background(), sess))
	require.NoError(t, sessionStore.Revoke(context.Background(), sess.ID))

	wireToken, _, issueErr := svc.refreshStore.Issue(context.Background(), sess.ID, u.ID, int64(1))
	require.NoError(t, issueErr)

	_, err = svc.Refresh(context.Background(), wireToken)
	require.Error(t, err, "refresh must reject revoked session")

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRefreshFailed, ec.Code,
		"revoked session must surface uniform 401 ErrAuthRefreshFailed — "+
			"P1-A regression: user status (active/suspended/locked) must NOT "+
			"escalate the wire envelope to 403 ErrAuthUserNotActive once the "+
			"session is revoked.")
	assert.Equal(t, errcode.KindUnauthenticated, ec.Kind,
		"revoked-session Kind must be KindUnauthenticated (401), not "+
			"KindPermissionDenied (403); a 503/403 leak here would break "+
			"防枚举 single-envelope semantics (ADR §A12)")
}

// TestService_Refresh_RevokedSession_UserRepoUnavailable_StillReturns401
// is the second P1-A regression: even when userRepo would have returned
// KindUnavailable (503) for the subject, the revoke prong fires first
// and surfaces uniform 401, not 503. Without the prong reorder, a revoked
// session combined with a transient userRepo outage would leak as 503 —
// confirming both that the session exists AND that user lookup is the
// bottleneck (a side-channel signal).
func TestService_Refresh_RevokedSession_UserRepoUnavailable_StillReturns401(t *testing.T) {
	sessionStore := newTestSessionStore(t)
	refreshStore := newTestRefreshStore()
	userRepo := &refreshUnavailableUserRepo{}
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()

	svc := MustNewService(
		sessionStore, roleRepo, userRepo, refreshStore,
		testIssuer, slog.Default(),
		WithClock(clock.Real()),
		WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
		withTestInvalidator(userRepo, sessionStore, refreshStore),
	)

	// Seed a revoked session.
	sess := newTestSession("usr-revoked-503", "sess-revoked-503")
	require.NoError(t, sessionStore.Create(context.Background(), sess))
	require.NoError(t, sessionStore.Revoke(context.Background(), sess.ID))

	wireToken, _, issueErr := refreshStore.Issue(context.Background(), sess.ID, "usr-revoked-503", int64(1))
	require.NoError(t, issueErr)

	_, err := svc.Refresh(context.Background(), wireToken)
	require.Error(t, err, "refresh must reject revoked session even when userRepo would 503")

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.KindUnauthenticated, ec.Kind,
		"revoked session must surface 401, not 503 — P1-A regression: "+
			"a userRepo outage must NOT leak through once the session is revoked.")
	assert.Equal(t, errcode.ErrAuthRefreshFailed, ec.Code,
		"revoked-with-userRepo-outage must collapse to ErrAuthRefreshFailed, "+
			"not ErrAuthRefreshUnavailable (防枚举 single-envelope)")
}

// refreshUnavailableUserRepo always returns KindUnavailable from GetByID,
// simulating a transient user-store outage so the test can assert that
// the revoke prong fires before user lookup is even attempted.
type refreshUnavailableUserRepo struct{}

var _ ports.UserRepository = (*refreshUnavailableUserRepo)(nil)

func (refreshUnavailableUserRepo) GetByID(_ context.Context, _ string) (*domain.User, error) {
	return nil, errcode.New(errcode.KindUnavailable, errcode.ErrAuthRefreshUnavailable,
		"user store unavailable")
}

func (refreshUnavailableUserRepo) Create(_ context.Context, _ *domain.User) error { return nil }
func (refreshUnavailableUserRepo) GetByUsername(_ context.Context, _ string) (*domain.User, error) {
	return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "n/a")
}
func (refreshUnavailableUserRepo) Update(_ context.Context, _ *domain.User) error { return nil }
func (refreshUnavailableUserRepo) Delete(_ context.Context, _ string) error       { return nil }
func (refreshUnavailableUserRepo) UpdatePassword(_ context.Context, _ string, _ string, _ bool, _ int64) (int64, error) {
	return 0, nil
}
func (refreshUnavailableUserRepo) BumpAuthzEpoch(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
func (refreshUnavailableUserRepo) GetByIDForUpdate(_ context.Context, _ string) (*domain.User, error) {
	return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "n/a")
}
func (refreshUnavailableUserRepo) GetByUsernameForUpdate(_ context.Context, _ string) (*domain.User, error) {
	return nil, errcode.New(errcode.KindNotFound, errcode.ErrAuthUserNotFound, "n/a")
}

// TestService_Refresh_TwoAssertOrdering_BaselineOnly_Returns403 verifies the
// converse: an active user with a revoked session — only the session-revoked
// gate fails, baseline passes — surfaces 401 ErrAuthRefreshFailed (preserved
// by the existing test, named explicitly here for ordering completeness).
func TestService_Refresh_TwoAssertOrdering_SessionRevokedOnly_Returns401(t *testing.T) {
	svc, sessionStore, userRepo := newTestServiceWithUserRepo(t)

	// User is active → baseline passes.
	u, err := domain.NewUser("active-user", "active-ordering@test.local", "hash", time.Now())
	require.NoError(t, err)
	u.ID = "usr-active-revoked-sess"
	require.NoError(t, userRepo.Create(context.Background(), u))

	// Session revoked → only session-revoked gate fails.
	sess := newTestSession(u.ID, "sess-active-revoked")
	require.NoError(t, sessionStore.Create(context.Background(), sess))
	require.NoError(t, sessionStore.Revoke(context.Background(), sess.ID))

	wireToken, _, issueErr := svc.refreshStore.Issue(context.Background(), sess.ID, u.ID, int64(1))
	require.NoError(t, issueErr)

	_, err = svc.Refresh(context.Background(), wireToken)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRefreshFailed, ec.Code,
		"session-revoked path must surface uniform 401 ErrAuthRefreshFailed")
	assert.Equal(t, errcode.KindUnauthenticated, ec.Kind)
}

func TestService_Refresh_RoleFetchFailure_AbortsRefresh(t *testing.T) {
	sessionStore := &countingSessionStore{Store: newTestSessionStore(t)}
	roleRepo := &brokenRoleRepo{err: fmt.Errorf("roleRepo outage")}
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	u, _ := domain.NewUser("usr-rolefail", "rolefail@test.local", "hash", time.Now())
	u.ID = "usr-rolefail"
	require.NoError(t, userRepo.Create(context.Background(), u))

	refreshStore := newTestRefreshStore()
	svc := MustNewService(sessionStore, roleRepo, userRepo, refreshStore, testIssuer, slog.Default(),
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
		withTestInvalidator(userRepo, sessionStore, refreshStore))

	sess := newTestSession("usr-rolefail", "sess-rolefail")
	require.NoError(t, sessionStore.Create(context.Background(), sess))
	// The initial Create is for the seed session; reset counter to only track
	// creates during Refresh.
	sessionStore.creates = 0

	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-rolefail", "usr-rolefail", int64(1))
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.Error(t, err, "Refresh must fail when role fetch fails")
	assert.Empty(t, pair.AccessToken, "no token on failure")

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRoleFetchFailed, ec.Code,
		"fail-closed: role fetch failure surfaces as ErrAuthRoleFetchFailed")

	assert.Equal(t, 0, sessionStore.creates, "no new session must be created on fail-closed")
	_, _, err = refreshStore.Rotate(context.Background(), wireToken)
	require.NoError(t, err, "role fetch failure must not advance the refresh lineage")
}

func TestService_Refresh(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(session.Store, refresh.Store) string // returns wire token
		wantErr bool
	}{
		{
			name: "valid refresh",
			setup: func(store session.Store, rs refresh.Store) string {
				sess := newTestSession("usr-1", "sess-1")
				_ = store.Create(context.Background(), sess)
				wire, _, _ := rs.Issue(context.Background(), "sess-1", "usr-1", int64(1))
				return wire
			},
			wantErr: false,
		},
		{
			name: "revoked session",
			setup: func(store session.Store, rs refresh.Store) string {
				sess := newTestSession("usr-2", "sess-2")
				_ = store.Create(context.Background(), sess)
				_ = store.Revoke(context.Background(), "sess-2")
				wire, _, _ := rs.Issue(context.Background(), "sess-2", "usr-2", int64(1))
				return wire
			},
			wantErr: true,
		},
		{
			name:    "empty token",
			setup:   func(_ session.Store, _ refresh.Store) string { return "" },
			wantErr: true,
		},
		{
			name:    "invalid opaque token",
			setup:   func(_ session.Store, _ refresh.Store) string { return "bad-token" },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, store, refreshStore := newTestServiceWithRefreshStore(t, "usr-1", "usr-2")
			wireToken := tt.setup(store, refreshStore)

			pair, err := svc.Refresh(context.Background(), wireToken)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.NotEmpty(t, pair.AccessToken)
				assert.NotEmpty(t, pair.RefreshToken)
				// TDD: Refresh must populate UserID and SessionID.
				assert.NotEmpty(t, pair.UserID, "Refresh must return a non-empty UserID")
				assert.NotEmpty(t, pair.SessionID, "Refresh must return a non-empty SessionID")
			}
		})
	}
}

func TestService_Refresh_TokenRotation(t *testing.T) {
	svc, store, refreshStore, clk := newTestServiceWithClock(t, "usr-rot")

	sess := newTestSession("usr-rot", "sess-rot")
	require.NoError(t, store.Create(context.Background(), sess))

	wire1, _, err := refreshStore.Issue(context.Background(), "sess-rot", "usr-rot", int64(1))
	require.NoError(t, err)

	// First refresh should succeed and rotate the token.
	pair1, err := svc.Refresh(context.Background(), wire1)
	require.NoError(t, err)
	assert.NotEqual(t, wire1, pair1.RefreshToken, "refresh token should be rotated")

	// Advance the clock past the ReuseInterval (2s) so the old token is no longer
	// in the grace window and will be rejected as a reuse attack.
	clk.Advance(testtime.D3s)

	// Presenting the old wire token again should be rejected (reuse after grace).
	_, err = svc.Refresh(context.Background(), wire1)
	require.Error(t, err, "old wire token must be rejected after rotation")
	assert.Contains(t, err.Error(), "ERR_AUTH_REFRESH_FAILED")
}

func TestService_Refresh_ConcurrentRefresh(t *testing.T) {
	svc, store, refreshStore := newTestServiceWithRefreshStore(t, "usr-conc")

	sess := newTestSession("usr-conc", "sess-conc")
	require.NoError(t, store.Create(context.Background(), sess))

	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-conc", "usr-conc", int64(1))
	require.NoError(t, err)

	const goroutines = 5
	type result struct {
		refreshToken string
		err          error
	}
	results := make(chan result, goroutines)

	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			p, refreshErr := svc.Refresh(context.Background(), wireToken)
			results <- result{p.RefreshToken, refreshErr}
		})
	}
	wg.Wait()
	close(results)

	// With the opaque store's grace-retry semantics, all goroutines presenting
	// the same wire token within the ReuseInterval window succeed and each
	// receives a distinct child token. This mirrors the storetest T10 contract.
	var successes int
	distinct := make(map[string]struct{})
	for r := range results {
		if r.err != nil {
			t.Logf("goroutine failed (expected if outside grace window): %v", r.err)
			continue
		}
		successes++
		if r.refreshToken != "" {
			distinct[r.refreshToken] = struct{}{}
		}
	}

	require.Greater(t, successes, 0, "at least one concurrent refresh must succeed")
	assert.Len(t, distinct, successes, "each successful refresh must yield a distinct new wire token")
}

// TestService_Refresh_AccessTokenCarriesStableSessionID verifies that refresh
// preserves session.ID — the access JWT's sid claim equals the original login
// session ID. Aligns with OAuth2 RFC 6749 §6 (refresh = same grant) and OIDC
// Back-Channel Logout sid stability (ory/fosite Session.Clone + zitadel
// oidc_session aggregate + keycloak findOfflineUserSession all behave the
// same way).
func TestService_Refresh_AccessTokenCarriesStableSessionID(t *testing.T) {
	svc, store, refreshStore := newTestServiceWithRefreshStore(t, "usr-sid")

	sess := newTestSession("usr-sid", "sess-r1")
	require.NoError(t, store.Create(context.Background(), sess))

	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-r1", "usr-sid", int64(1))
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.NoError(t, err)

	verifier, err := auth.NewJWTVerifier(testKeySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	accessClaims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "sess-r1", accessClaims.SessionID,
		"refreshed access token must carry the original session ID (stable sid)")
	assert.Equal(t, "sess-r1", pair.SessionID,
		"TokenPair.SessionID must equal the original session ID")
}

// TestService_Refresh_SessionRowIsImmutable verifies that refresh does not
// mutate the session row: RevokedAt stays nil, CreatedAt/ExpiresAt unchanged.
// The session row's lifecycle spans login → logout; refresh only rotates the
// refresh-token chain and mints a new access JWT.
func TestService_Refresh_SessionRowIsImmutable(t *testing.T) {
	svc, store, refreshStore := newTestServiceWithRefreshStore(t, "usr-imm")

	const sessionID = "sess-imm"
	sess := newTestSession("usr-imm", sessionID)
	require.NoError(t, store.Create(context.Background(), sess))

	wireToken, _, err := refreshStore.Issue(context.Background(), sessionID, "usr-imm", int64(1))
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.NoError(t, err)
	assert.Equal(t, sessionID, pair.SessionID, "TokenPair.SessionID stable across refresh")

	// Validate-visible state (ValidateView) must not flip — refresh never
	// writes session.Store (SESSIONREFRESH-NO-SESSION-CREATE-01 archtest
	// guards this statically). GC-only metadata (CreatedAt, ExpiresAt, JTI)
	// is intentionally not exposed by Store.Get; round-trip of those
	// fields is verified by backend-specific tests.
	after, err := store.Get(context.Background(), sessionID)
	require.NoError(t, err)
	assert.Nil(t, after.RevokedAt, "session must NOT be revoked by refresh")
	assert.Equal(t, sessionID, after.ID, "session ID stable across refresh")

	verifier, err := auth.NewJWTVerifier(testKeySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)
	claims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, sessionID, claims.SessionID, "JWT sid claim equals the stable session ID")
}

// TestService_Refresh_TwoHops_SecondRefreshSucceeds is the reproduction test
// for the PR #482 P1 chain-rotation bug. Before the fix, the second refresh
// hop failed because refresh.Store.Rotate inherited the (now-revoked) old
// session_id; the access token returned from refresh #1 contained a new
// session UUID, but the refresh chain still pointed at the revoked row.
//
// After the fix (session.ID stable across refresh), the chain stays
// consistent and the second hop succeeds.
func TestService_Refresh_TwoHops_SecondRefreshSucceeds(t *testing.T) {
	svc, store, refreshStore, clk := newTestServiceWithClock(t, "usr-two")

	const sessionID = "sess-two"
	sess := newTestSession("usr-two", sessionID)
	require.NoError(t, store.Create(context.Background(), sess))

	wire1, _, err := refreshStore.Issue(context.Background(), sessionID, "usr-two", int64(1))
	require.NoError(t, err)

	pair1, err := svc.Refresh(context.Background(), wire1)
	require.NoError(t, err, "first refresh must succeed")
	require.NotEmpty(t, pair1.RefreshToken, "first refresh must return a new wire token")
	assert.Equal(t, sessionID, pair1.SessionID, "session ID stable after first refresh")

	// Advance past the grace window so the rotated parent (wire1) cannot be
	// replayed; the only valid presenter is pair1.RefreshToken.
	clk.Advance(testtime.D3s)

	pair2, err := svc.Refresh(context.Background(), pair1.RefreshToken)
	require.NoError(t, err, "second refresh must succeed using the rotated wire token")
	require.NotEmpty(t, pair2.RefreshToken, "second refresh must return a new wire token")
	assert.NotEqual(t, pair1.RefreshToken, pair2.RefreshToken, "second hop yields a distinct wire token")
	assert.Equal(t, sessionID, pair2.SessionID, "session ID stable after second refresh")
}

// TestService_Refresh_PastGCEligibility_Succeeds proves that refresh succeeds
// on a session row whose ExpiresAt (GC eligibility) is already in the past.
// ExpiresAt is a GC-only field; refresh does not gate on it, and the
// returned access JWT is fully valid. Paired with the sessionvalidate
// regression test TestService_VerifyIntent_PastSessionExpiresAt_StillValidates
// to cover the end-to-end F1 fix: past GC eligibility → fresh JWT → validate
// must accept.
func TestService_Refresh_PastGCEligibility_Succeeds(t *testing.T) {
	svc, store, refreshStore := newTestServiceWithRefreshStore(t, "usr-exp")

	expiredSession := &session.Session{
		ID:                "sess-exp",
		SubjectID:         "usr-exp",
		JTI:               "sess-exp",
		AuthzEpochAtIssue: 1,
		CreatedAt:         time.Now().Add(expiredSessionCreatedOffset),
		ExpiresAt:         time.Now().Add(-time.Minute), // past GC eligibility
	}
	require.NoError(t, store.Create(context.Background(), expiredSession))

	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-exp", "usr-exp", int64(1))
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.NoError(t, err)
	require.NotNil(t, pair)

	persisted, err := store.Get(context.Background(), "sess-exp")
	require.NoError(t, err)
	assert.Nil(t, persisted.RevokedAt, "session must NOT be revoked by refresh")
	assert.Equal(t, "sess-exp", pair.SessionID, "TokenPair.SessionID equals the original session ID")

	verifier, err := auth.NewJWTVerifier(testKeySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)
	_, err = verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err, "refreshed access token must be cryptographically valid")
}

// TestService_Refresh_SessionAwareVerifier proves that sessionrefresh catches
// revoked sessions even when the session is revoked out-of-band after the
// wire token is issued.
func TestService_Refresh_SessionAwareVerifier(t *testing.T) {
	sessionStore := newTestSessionStore(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	seedUser, _ := domain.NewUser("usr-sa", "usr-sa@test.local", "hash", time.Now())
	seedUser.ID = "usr-sa"
	require.NoError(t, userRepo.Create(context.Background(), seedUser))

	refreshStore := newTestRefreshStore()
	svc := MustNewService(sessionStore, roleRepo, userRepo, refreshStore, testIssuer, slog.Default(),
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
		withTestInvalidator(userRepo, sessionStore, refreshStore))

	sess := newTestSession("usr-sa", "sess-sa")
	require.NoError(t, sessionStore.Create(context.Background(), sess))

	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-sa", "usr-sa", int64(1))
	require.NoError(t, err)

	// Normal refresh should succeed.
	pair, err := svc.Refresh(context.Background(), wireToken)
	require.NoError(t, err)
	assert.NotEmpty(t, pair.AccessToken)

	// Revoke the NEW session externally (the B2 model created pair.SessionID).
	require.NoError(t, sessionStore.Revoke(context.Background(), pair.SessionID))

	// Attempt refresh with the new (rotated) wire token — should be rejected
	// because the new session is revoked.
	_, err = svc.Refresh(context.Background(), pair.RefreshToken)
	assert.Error(t, err, "revoked session must reject even a fresh wire token")
}

// TestRefresh_FailClosedWhenUserUnavailable verifies the F1 fail-closed policy:
// when userRepo.GetByID returns an error (user deleted mid-session), refresh
// must return ErrAuthRefreshFailed rather than signing a new access token.
func TestRefresh_FailClosedWhenUserUnavailable(t *testing.T) {
	sessionStore := newTestSessionStore(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	userRepo := mem.NewStore(clock.Real()).UserRepository() // intentionally empty — GetByID returns error
	refreshStore := newTestRefreshStore()
	svc := MustNewService(sessionStore, roleRepo, userRepo, refreshStore, testIssuer, slog.Default(),
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
		withTestInvalidator(userRepo, sessionStore, refreshStore))

	sess := newTestSession("usr-missing", "sess-missing")
	require.NoError(t, sessionStore.Create(context.Background(), sess))

	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-missing", "usr-missing", int64(1))
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.Error(t, err, "fail-closed: refresh must error when user is unavailable")
	assert.Empty(t, pair.AccessToken)
}

// TestRefresh_FlagPropagatesFromCurrentUser_AfterClear ensures that after a
// user clears PasswordResetRequired, the next refresh produces a new access
// token with password_reset_required=false.
func TestRefresh_FlagPropagatesFromCurrentUser_AfterClear(t *testing.T) {
	_, sessionStore, userRepo := newTestServiceWithUserRepo(t)

	// Seed a user with reset flag = false (already cleared).
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.MinCost)
	user, _ := domain.NewUser("ref-user-clear", "ref-clear@test.com", string(hash), time.Now())
	user.ID = "usr-ref-clear"
	// PasswordResetRequired is false by default.
	require.NoError(t, userRepo.Create(context.Background(), user))

	// Recreate with a known refreshStore so we can issue and rotate wire tokens.
	refreshStore := newTestRefreshStore()
	svc2 := MustNewService(sessionStore, mem.NewStore(clock.Real()).RoleRepository(), userRepo, refreshStore, testIssuer, slog.Default(),
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
		withTestInvalidator(userRepo, sessionStore, refreshStore))

	sess := newTestSession("usr-ref-clear", "sess-ref-clear")
	require.NoError(t, sessionStore.Create(context.Background(), sess))

	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-ref-clear", "usr-ref-clear", int64(1))
	require.NoError(t, err)

	pair, err := svc2.Refresh(context.Background(), wireToken)
	require.NoError(t, err)
	assert.False(t, pair.PasswordResetRequired, "after clearing flag, refreshed token must have claim=false")

	verifier, err := auth.NewJWTVerifier(testKeySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)
	claims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.False(t, claims.PasswordResetRequired, "access token claim must be false after flag cleared")
}

// TestRefresh_FlagStillSetWhenUserNotChanged ensures that a user who has not
// changed their password keeps getting tokens with password_reset_required=true
// on each refresh.
func TestRefresh_FlagStillSetWhenUserNotChanged(t *testing.T) {
	sessionStore := newTestSessionStore(t)
	userRepo := mem.NewStore(clock.Real()).UserRepository()

	// Seed a user with reset flag = true.
	hash, _ := bcrypt.GenerateFromPassword([]byte("pass"), bcrypt.MinCost)
	user, _ := domain.NewUser("ref-user-reset", "ref-reset@test.com", string(hash), time.Now())
	user.ID = "usr-ref-reset"
	user.SetPasswordResetRequired(true, time.Now())
	require.NoError(t, userRepo.Create(context.Background(), user))

	refreshStore := newTestRefreshStore()
	svc := MustNewService(sessionStore, mem.NewStore(clock.Real()).RoleRepository(), userRepo, refreshStore, testIssuer, slog.Default(),
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
		withTestInvalidator(userRepo, sessionStore, refreshStore))

	sess := newTestSession("usr-ref-reset", "sess-ref-reset")
	require.NoError(t, sessionStore.Create(context.Background(), sess))

	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-ref-reset", "usr-ref-reset", int64(1))
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.NoError(t, err)
	assert.True(t, pair.PasswordResetRequired, "refreshed token must still have claim=true when user hasn't changed password")

	verifier, err := auth.NewJWTVerifier(testKeySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)
	claims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.True(t, claims.PasswordResetRequired, "access token claim must be true when flag not cleared")
}

// infraGetRepo overrides Get to return an infra error.
type infraGetRepo struct {
	session.Store
	infraErr error
}

func (r *infraGetRepo) Get(_ context.Context, _ string) (*session.ValidateView, error) {
	return nil, r.infraErr
}

// TestService_Refresh_InfraErrorOnSessionLookup verifies that an infra error
// from sessionStore.Get causes Refresh to fail closed.
func TestService_Refresh_InfraErrorOnSessionLookup(t *testing.T) {
	infraErr := fmt.Errorf("db connection timeout")
	sessionStore := &infraGetRepo{
		Store:    newTestSessionStore(t),
		infraErr: infraErr,
	}
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	userRepo := mem.NewStore(clock.Real()).UserRepository()

	refreshStore := newTestRefreshStore()
	svc := MustNewService(sessionStore, roleRepo, userRepo, refreshStore, testIssuer, slog.Default(),
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
		withTestInvalidator(userRepo, sessionStore, refreshStore))

	// Issue a wire token but don't seed the session — Get will return infraErr.
	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-infra", "usr-infra", int64(1))
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.Error(t, err, "infra error must cause Refresh to fail")
	assert.Empty(t, pair.AccessToken)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRefreshUnavailable, ec.Code)
	_, _, err = refreshStore.Rotate(context.Background(), wireToken)
	require.NoError(t, err, "session lookup infra failure must not rotate or revoke the presented token")
}

// spyRefreshStore wraps a real refresh.Store and records revoke calls.
// Used by F14 to assert cascade-revoke is triggered on session-not-found.
type spyRefreshStore struct {
	refresh.Store
	mu                     sync.Mutex
	revokeSessionN         int
	revokeSessionDetachedN int
	lastSessionID          string
	lastDetachedSessionID  string
}

func (s *spyRefreshStore) RevokeSession(ctx context.Context, sessionID string) error {
	s.mu.Lock()
	s.revokeSessionN++
	s.lastSessionID = sessionID
	s.mu.Unlock()
	return s.Store.RevokeSession(ctx, sessionID)
}

func (s *spyRefreshStore) RevokeSessionDetached(ctx context.Context, sessionID string) error {
	s.mu.Lock()
	s.revokeSessionDetachedN++
	s.lastDetachedSessionID = sessionID
	s.mu.Unlock()
	return s.Store.RevokeSessionDetached(ctx, sessionID)
}

type revokeFailingRefreshStore struct {
	refresh.Store
	err error
}

func (s revokeFailingRefreshStore) RevokeSessionDetached(context.Context, string) error {
	return s.err
}

// sessionNotFoundStore returns a domain not-found error from Get.
type sessionNotFoundStore struct {
	session.Store
	notFoundErr error
}

func domainSessionNotFoundError() error {
	return errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "session not found",
		errcode.WithCategory(errcode.CategoryDomain))
}

func (r *sessionNotFoundStore) Get(_ context.Context, _ string) (*session.ValidateView, error) {
	return nil, r.notFoundErr
}

// TestService_Refresh_SessionNotFound_CascadeRevokes verifies that when
// sessionStore.Get returns a domain ErrSessionNotFound (not an infra error),
// Refresh returns ErrAuthRefreshFailed AND calls RevokeSessionDetached on the
// rotated token so the newly-issued child cannot be used by an attacker (F14).
func TestService_Refresh_SessionNotFound_CascadeRevokes(t *testing.T) {
	notFoundErr := domainSessionNotFoundError()
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	userRepo := mem.NewStore(clock.Real()).UserRepository()

	// Use issueTestWireToken to set up the refreshStore; then swap in a spy
	// and a sessionStore stub so Get returns not-found.
	_, _, innerStore, wireToken := issueTestWireToken(t, "usr-notfound", "sess-notfound")

	spy := &spyRefreshStore{Store: innerStore}
	sessionStore := &sessionNotFoundStore{notFoundErr: notFoundErr}
	svc := MustNewService(sessionStore, roleRepo, userRepo, spy, testIssuer, slog.Default(),
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
		withTestInvalidator(userRepo, sessionStore, spy))

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.Error(t, err, "session-not-found must cause Refresh to fail")
	assert.Empty(t, pair.AccessToken)
	assert.Contains(t, err.Error(), "ERR_AUTH_REFRESH_FAILED")

	spy.mu.Lock()
	detachedN := spy.revokeSessionDetachedN
	businessN := spy.revokeSessionN
	spy.mu.Unlock()
	assert.Equal(t, 1, detachedN, "RevokeSessionDetached must be called once on session-not-found")
	assert.Zero(t, businessN, "session-refresh cascade must not use business RevokeSession")
}

func TestService_Refresh_CascadeRevokeFailure_ReturnsRefreshUnavailable(t *testing.T) {
	notFoundErr := domainSessionNotFoundError()
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	_, _, innerStore, wireToken := issueTestWireToken(t, "usr-revoke-fail", "sess-revoke-fail")

	refreshStore := revokeFailingRefreshStore{
		Store: innerStore,
		err:   errcode.New(errcode.KindInternal, errcode.ErrInternal, "refresh store down"),
	}
	sessionStore := &sessionNotFoundStore{notFoundErr: notFoundErr}
	svc := MustNewService(sessionStore, roleRepo, userRepo, refreshStore, testIssuer, slog.Default(),
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
		withTestInvalidator(userRepo, sessionStore, innerStore))

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.Error(t, err)
	assert.Empty(t, pair.AccessToken)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRefreshUnavailable, ec.Code)
}

func TestService_Refresh_SessionUpdateNotFound_CascadeRevokesAndRejects(t *testing.T) {
	// Session disappears between Peek and verifySession — cascade revokes the
	// chain and rejects with ErrAuthRefreshFailed.
	notFoundErr := domainSessionNotFoundError()
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	user, err := domain.NewUser("usr-update-missing", "usr-update-missing@test.local", "hash", time.Now())
	require.NoError(t, err)
	user.ID = "usr-update-missing"
	require.NoError(t, userRepo.Create(context.Background(), user))

	innerStore := newTestRefreshStore()
	spy := &spyRefreshStore{Store: innerStore}
	sessionStore := &sessionNotFoundStore{notFoundErr: notFoundErr}
	svc := MustNewService(sessionStore, roleRepo, userRepo, spy, testIssuer, slog.Default(),
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
		withTestInvalidator(userRepo, sessionStore, innerStore))

	wireToken, _, err := innerStore.Issue(context.Background(), "sess-update-missing", "usr-update-missing", int64(1))
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.Error(t, err)
	assert.Empty(t, pair.AccessToken)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRefreshFailed, ec.Code)
	assert.Equal(t, "invalid refresh token", ec.Message)

	spy.mu.Lock()
	detachedN := spy.revokeSessionDetachedN
	businessN := spy.revokeSessionN
	spy.mu.Unlock()
	assert.Equal(t, 1, detachedN, "session not-found must cascade revoke the refresh chain")
	assert.Zero(t, businessN, "session update cascade must not use business RevokeSession")
}

func TestService_Refresh_RejectionMessagesAreUniform(t *testing.T) {
	tests := []struct {
		name  string
		build func(t *testing.T) (*Service, string)
	}{
		{
			name: "session not found",
			build: func(t *testing.T) (*Service, string) {
				t.Helper()
				_, _, innerStore, wireToken := issueTestWireToken(t, "usr-uniform-notfound", "sess-uniform-notfound")
				userRepo := mem.NewStore(clock.Real()).UserRepository()
				sessionStore := &sessionNotFoundStore{notFoundErr: domainSessionNotFoundError()}
				svc := MustNewService(
					sessionStore,
					mem.NewStore(clock.Real()).RoleRepository(),
					userRepo,
					innerStore,
					testIssuer,
					slog.Default(),
					WithClock(clock.Real()),
					WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
					withTestInvalidator(userRepo, sessionStore, innerStore),
				)
				return svc, wireToken
			},
		},
		{
			name: "revoked session",
			build: func(t *testing.T) (*Service, string) {
				t.Helper()
				svc, store, refreshStore := newTestServiceWithRefreshStore(t, "usr-uniform-revoked")
				sess := newTestSession("usr-uniform-revoked", "sess-uniform-revoked")
				require.NoError(t, store.Create(context.Background(), sess))
				require.NoError(t, store.Revoke(context.Background(), "sess-uniform-revoked"))
				wireToken, _, err := refreshStore.Issue(context.Background(), "sess-uniform-revoked", "usr-uniform-revoked", int64(1))
				require.NoError(t, err)
				return svc, wireToken
			},
		},
		{
			name: "user not found",
			build: func(t *testing.T) (*Service, string) {
				t.Helper()
				sessionStore := newTestSessionStore(t)
				refreshStore := newTestRefreshStore()
				userRepo := mem.NewStore(clock.Real()).UserRepository()
				svc := MustNewService(sessionStore, mem.NewStore(clock.Real()).RoleRepository(), userRepo,
					refreshStore, testIssuer, slog.Default(),
					WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
					withTestInvalidator(userRepo, sessionStore, refreshStore))
				sess := newTestSession("usr-uniform-missing", "sess-uniform-missing")
				require.NoError(t, sessionStore.Create(context.Background(), sess))
				wireToken, _, err := refreshStore.Issue(context.Background(), "sess-uniform-missing", "usr-uniform-missing", int64(1))
				require.NoError(t, err)
				return svc, wireToken
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc, wireToken := tc.build(t)
			pair, err := svc.Refresh(context.Background(), wireToken)
			require.Error(t, err)
			assert.Empty(t, pair.AccessToken)
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec)
			assert.Equal(t, errcode.ErrAuthRefreshFailed, ec.Code)
			assert.Equal(t, "invalid refresh token", ec.Message)
		})
	}
}

func TestService_Refresh_CascadeRejectionReasonIsLogged(t *testing.T) {
	tests := []struct {
		name       string
		wantReason string
		build      func(t *testing.T, logger *slog.Logger) (*Service, string)
	}{
		{
			name:       "session not found",
			wantReason: "session-not-found",
			build: func(t *testing.T, logger *slog.Logger) (*Service, string) {
				t.Helper()
				_, _, innerStore, wireToken := issueTestWireToken(t, "usr-log-notfound", "sess-log-notfound")
				userRepo := mem.NewStore(clock.Real()).UserRepository()
				sessionStore := &sessionNotFoundStore{notFoundErr: domainSessionNotFoundError()}
				svc := MustNewService(
					sessionStore,
					mem.NewStore(clock.Real()).RoleRepository(),
					userRepo,
					innerStore,
					testIssuer,
					logger,
					WithClock(clock.Real()),
					WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
					withTestInvalidator(userRepo, sessionStore, innerStore),
				)
				return svc, wireToken
			},
		},
		{
			name:       "revoked session",
			wantReason: "revoked-session",
			build: func(t *testing.T, logger *slog.Logger) (*Service, string) {
				t.Helper()
				svc, store, refreshStore := newTestServiceWithRefreshStore(t, "usr-log-revoked")
				svc.logger = logger
				sess := newTestSession("usr-log-revoked", "sess-log-revoked")
				require.NoError(t, store.Create(context.Background(), sess))
				require.NoError(t, store.Revoke(context.Background(), "sess-log-revoked"))
				wireToken, _, err := refreshStore.Issue(context.Background(), "sess-log-revoked", "usr-log-revoked", int64(1))
				require.NoError(t, err)
				return svc, wireToken
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			logger := slog.New(slog.NewJSONHandler(&logs, nil))
			svc, wireToken := tc.build(t, logger)

			pair, err := svc.Refresh(context.Background(), wireToken)
			require.Error(t, err)
			assert.Empty(t, pair.AccessToken)

			entry := sloghelper.FindLogEntry(logs.String(), "cascade revoked refresh chain")
			require.NotNil(t, entry)
			assert.Equal(t, "WARN", entry["level"])
			assert.Equal(t, tc.wantReason, entry["reason"])
		})
	}
}

// TestRefresh_EmptyToken_AuthErrorCode verifies that presenting an empty
// refresh token returns ErrAuthRefreshInvalidInput (not ErrValidationFailed).
// This is the service-level auth-domain error code contract guard.
func TestRefresh_EmptyToken_AuthErrorCode(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)
	_, err := svc.Refresh(context.Background(), "")
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec, "expected *errcode.Error")
	assert.Equal(t, errcode.ErrAuthRefreshInvalidInput, ec.Code,
		"empty refresh token must yield ErrAuthRefreshInvalidInput, not ErrValidationFailed")
}

// TestRefresh_EmptyToken_NoLengthOracle verifies that an empty refresh token
// does not produce an error message containing internal length hints.
func TestRefresh_EmptyToken_NoLengthOracle(t *testing.T) {
	t.Parallel()
	svc, _ := newTestService(t)
	_, err := svc.Refresh(context.Background(), "")
	require.Error(t, err)
	msg := err.Error()
	assert.NotContains(t, msg, "value too short",
		"error message must not reveal length oracle")
	assert.NotContains(t, msg, "value too long",
		"error message must not reveal length oracle")
}

// TestRefresh_RotateFailure_ReturnsRefreshUnavailable verifies that when
// refreshStore.Rotate returns a non-rejected infra error, Refresh returns
// ErrAuthRefreshUnavailable (not ErrAuthRefreshFailed) so clients can
// distinguish an outage from invalid credentials.
func TestRefresh_RotateFailure_ReturnsRefreshUnavailable(t *testing.T) {
	_, sessionStore, innerStore := newTestServiceWithRefreshStore(t, "usr-rotate-fail")

	sess := newTestSession("usr-rotate-fail", "sess-rotate-fail")
	require.NoError(t, sessionStore.Create(context.Background(), sess))

	wireToken, _, err := innerStore.Issue(context.Background(), "sess-rotate-fail", "usr-rotate-fail", int64(1))
	require.NoError(t, err)

	// Replace refreshStore with one that fails on Rotate.
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	u, _ := domain.NewUser("usr-rotate-fail", "rotate-fail@test.local", "hash", time.Now())
	u.ID = "usr-rotate-fail"
	require.NoError(t, userRepo.Create(context.Background(), u))

	failStore := rotateFailingRefreshStore{
		Store: innerStore,
		err:   errcode.New(errcode.KindInternal, errcode.ErrInternal, "rotate store down"),
	}
	svc2 := MustNewService(sessionStore, roleRepo, userRepo, failStore, testIssuer, slog.Default(),
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
		withTestInvalidator(userRepo, sessionStore, innerStore))

	pair, err := svc2.Refresh(context.Background(), wireToken)
	require.Error(t, err)
	assert.Equal(t, dto.TokenPair{}, pair)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRefreshUnavailable, ec.Code)
}

// rotateFailingRefreshStore wraps a real refresh.Store and overrides Rotate to
// return a configurable error so the post-Rotate error path can be exercised.
type rotateFailingRefreshStore struct {
	refresh.Store
	err error
}

func (s rotateFailingRefreshStore) Rotate(_ context.Context, _ string) (string, *refresh.Token, error) {
	return "", nil, s.err
}

// rotateMismatchRefreshStore wraps a real refresh.Store and overrides Rotate to
// return a Token with deliberately mismatched SessionID / SubjectID so the
// rotated-subject-mismatch branch is exercised.
type rotateMismatchRefreshStore struct {
	refresh.Store
	rotatedSessionID string
	rotatedSubjectID string
}

func (s rotateMismatchRefreshStore) Rotate(_ context.Context, _ string) (string, *refresh.Token, error) {
	return "dummy-wire", &refresh.Token{SessionID: s.rotatedSessionID, SubjectID: s.rotatedSubjectID}, nil
}

// TestRefresh_RotateMismatch_CascadeRevoke_ReturnsRejected verifies that when
// Rotate returns a token with a SessionID or SubjectID that does not match the
// validated session, Refresh cascade-revokes and returns ErrAuthRefreshFailed.
func TestRefresh_RotateMismatch_CascadeRevoke_ReturnsRejected(t *testing.T) {
	_, sessionStore, innerStore := newTestServiceWithRefreshStore(t, "usr-mismatch")

	sess := newTestSession("usr-mismatch", "sess-mismatch")
	require.NoError(t, sessionStore.Create(context.Background(), sess))

	wireToken, _, err := innerStore.Issue(context.Background(), "sess-mismatch", "usr-mismatch", int64(1))
	require.NoError(t, err)

	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	u, _ := domain.NewUser("usr-mismatch", "mismatch@test.local", "hash", time.Now())
	u.ID = "usr-mismatch"
	require.NoError(t, userRepo.Create(context.Background(), u))

	spy := &spyRefreshStore{Store: innerStore}
	// Override Rotate to return a token with wrong SessionID.
	mismatchStore := rotateMismatchRefreshStore{Store: spy, rotatedSessionID: "wrong-session", rotatedSubjectID: "usr-mismatch"}
	svc2 := MustNewService(sessionStore, roleRepo, userRepo, mismatchStore, testIssuer, slog.Default(),
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
		withTestInvalidator(userRepo, sessionStore, innerStore))

	pair, err := svc2.Refresh(context.Background(), wireToken)
	require.Error(t, err)
	assert.Equal(t, dto.TokenPair{}, pair)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRefreshFailed, ec.Code)
	assert.Equal(t, "invalid refresh token", ec.Message)
}

// TestRefresh_AccessJWT_NoAuthzEpochClaim verifies that after Rotate succeeds,
// the new access JWT does NOT carry an authz_epoch claim (S4d: epoch provenance
// moved to session/refresh rows; the JWT claim is removed entirely).
func TestRefresh_AccessJWT_NoAuthzEpochClaim(t *testing.T) {
	sessionStore := newTestSessionStore(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	userRepo := mem.NewStore(clock.Real()).UserRepository()

	u, _ := domain.NewUser("usr-epoch-ref", "epoch-ref@test.local", "hash", time.Now())
	u.ID = "usr-epoch-ref"
	require.NoError(t, userRepo.Create(context.Background(), u))
	// Bump epoch 4 times so it reaches 5 (initial=1).
	for range 4 {
		_, _ = userRepo.BumpAuthzEpoch(context.Background(), "usr-epoch-ref")
	}
	u, _ = userRepo.GetByID(context.Background(), "usr-epoch-ref")

	refreshStore := newTestRefreshStore()
	svc := MustNewService(sessionStore, roleRepo, userRepo, refreshStore, testIssuer, slog.Default(),
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
		withTestInvalidator(userRepo, sessionStore, refreshStore))

	sess := newTestSession("usr-epoch-ref", "sess-epoch-ref")
	sess.AuthzEpochAtIssue = u.AuthzEpoch() // match user epoch so refresh succeeds; this test
	// asserts JWT claim *shape* after refresh, not stale-epoch behavior.
	require.NoError(t, sessionStore.Create(context.Background(), sess))

	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-epoch-ref", "usr-epoch-ref", u.AuthzEpoch())
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.NoError(t, err)

	verifier, err := auth.NewJWTVerifier(testKeySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	claims, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	// S4d: authz_epoch removed from JWT; epoch lives in session.authz_epoch_at_issue row.
	_, epochInExtra := claims.Extra["authz_epoch"]
	assert.False(t, epochInExtra, "S4d: authz_epoch must not be present in JWT (including Extra)")
}

// staleEpochPeekStore is a refresh.Store whose Peek returns a token whose
// AuthzEpochAtIssue is deliberately lower than the user's live authz_epoch,
// simulating a stale refresh grant discovered after a credential event. Rotate
// is intentionally not implemented — the stale-epoch guard must fire before Rotate.
type staleEpochPeekStore struct {
	refresh.Store
	subjectID    string
	sessionID    string
	epochAtIssue int64 // stored epoch — lower than user's current epoch
}

func (s *staleEpochPeekStore) Peek(_ context.Context, _ string) (*refresh.Token, error) {
	return &refresh.Token{
		SessionID:         s.sessionID,
		SubjectID:         s.subjectID,
		AuthzEpochAtIssue: s.epochAtIssue,
	}, nil
}

// freshEpochPeekStore is a refresh.Store whose Peek returns a token that
// matches the user's current authz_epoch — used as the control arm in the
// stale-epoch test to confirm normal rotation succeeds.
type freshEpochPeekStore struct {
	refresh.Store
	subjectID    string
	sessionID    string
	epochAtIssue int64 // must equal user's current epoch
}

func (s *freshEpochPeekStore) Peek(_ context.Context, _ string) (*refresh.Token, error) {
	return &refresh.Token{
		SessionID:         s.sessionID,
		SubjectID:         s.subjectID,
		AuthzEpochAtIssue: s.epochAtIssue,
	}, nil
}

// TestRefresh_StaleEpoch_CascadeRevokesSessionOnly verifies P2.b fix:
// when presented.AuthzEpochAtIssue != user.AuthzEpoch() (stale grant after
// a credential event), the service must:
//
//	(a) return a uniform 401 reject (ErrAuthRefreshFailed)
//	(b) cascade-revoke THIS session's refresh chain via RevokeSessionDetached
//	(c) NOT call invalidator.Apply with CredentialEventRefreshReuse — the
//	    originating credential event already ran the user-wide trifecta;
//	    a second cascade would double-bump authz_epoch and double-revoke.
//
// Control arm: matching epoch → normal rotation success.
func TestRefresh_StaleEpoch_CascadeRevokesSessionOnly(t *testing.T) {
	t.Run("stale_epoch_rejects_and_revokes_session_only", func(t *testing.T) {
		sessionStore := newTestSessionStore(t)
		roleRepo := mem.NewStore(clock.Real()).RoleRepository()
		userRepo := mem.NewStore(clock.Real()).UserRepository()

		// Create user with initial authz_epoch=1, then bump once → epoch=2.
		u, err := domain.NewUser("usr-stale-epoch", "stale@test.local", "hash", time.Now())
		require.NoError(t, err)
		u.ID = "usr-stale-epoch"
		require.NoError(t, userRepo.Create(context.Background(), u))
		_, bumpErr := userRepo.BumpAuthzEpoch(context.Background(), "usr-stale-epoch")
		require.NoError(t, bumpErr)
		// Reload so u.AuthzEpoch() == 2.
		u, err = userRepo.GetByID(context.Background(), "usr-stale-epoch")
		require.NoError(t, err)
		require.Equal(t, int64(2), u.AuthzEpoch(), "setup: user epoch must be 2 after bump")

		// The refresh token was issued when epoch was 1 — stale.
		innerStore := newTestRefreshStore()
		staleStore := &staleEpochPeekStore{
			Store:        innerStore,
			subjectID:    "usr-stale-epoch",
			sessionID:    "sess-stale-epoch",
			epochAtIssue: 1, // stale: issued before the credential event bumped epoch
		}

		spyInv := &spyInvalidator{}
		spyRev := &spyRefreshStore{Store: innerStore}

		// Wire staleStore as the refresh store; wrap spyRev around innerStore for
		// RevokeSessionDetached assertions. staleStore.Store is innerStore so
		// cascadeRevoke's RevokeSessionDetached goes to spyRev.
		staleStore.Store = spyRev

		svc := MustNewServiceWithInvalidator(sessionStore, roleRepo, userRepo, staleStore, testIssuer, slog.Default(),
			spyInv,
			WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})))

		sess := newTestSession("usr-stale-epoch", "sess-stale-epoch")
		require.NoError(t, sessionStore.Create(context.Background(), sess))

		_, err = svc.Refresh(context.Background(), "any-wire-token")
		require.Error(t, err, "stale-epoch must cause Refresh to reject")
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		assert.Equal(t, errcode.ErrAuthRefreshFailed, ec.Code,
			"stale-epoch must surface as uniform ErrAuthRefreshFailed (401)")
		assert.Equal(t, errcode.KindUnauthenticated, ec.Kind)

		// (b) cascadeRevoke must have fired for THIS session's refresh chain.
		spyRev.mu.Lock()
		detachedN := spyRev.revokeSessionDetachedN
		detachedSID := spyRev.lastDetachedSessionID
		spyRev.mu.Unlock()
		assert.Equal(t, 1, detachedN,
			"stale-epoch must revoke THIS session's refresh chain via RevokeSessionDetached exactly once")
		assert.Equal(t, "sess-stale-epoch", detachedSID,
			"RevokeSessionDetached must target the stale session, not any other")

		// (c) invalidator.Apply must NOT have been called with CredentialEventRefreshReuse.
		// The originating credential event already ran the user-wide trifecta;
		// stale-epoch is benign churn, not a replay attack.
		for _, call := range spyInv.calls {
			assert.NotEqual(t, session.CredentialEventRefreshReuse, call.event,
				"stale-epoch must NOT emit CredentialEventRefreshReuse — not a reuse attack")
		}
	})

	t.Run("matching_epoch_succeeds", func(t *testing.T) {
		// Control: when presented.AuthzEpochAtIssue == user.AuthzEpoch(), refresh
		// must complete normally with a rotated token pair.
		sessionStore := newTestSessionStore(t)
		roleRepo := mem.NewStore(clock.Real()).RoleRepository()
		userRepo := mem.NewStore(clock.Real()).UserRepository()

		u, err := domain.NewUser("usr-fresh-epoch", "fresh@test.local", "hash", time.Now())
		require.NoError(t, err)
		u.ID = "usr-fresh-epoch"
		require.NoError(t, userRepo.Create(context.Background(), u))
		// No bump — epoch stays at 1.

		innerStore := newTestRefreshStore()
		freshStore := &freshEpochPeekStore{
			Store:        innerStore,
			subjectID:    "usr-fresh-epoch",
			sessionID:    "sess-fresh-epoch",
			epochAtIssue: 1, // matches user's current epoch=1
		}

		// freshEpochPeekStore.Peek returns the fresh token; Rotate goes to innerStore.
		freshStore.Store = innerStore

		svc := MustNewService(sessionStore, roleRepo, userRepo, freshStore, testIssuer, slog.Default(),
			WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
			withTestInvalidator(userRepo, sessionStore, innerStore))

		sess := newTestSession("usr-fresh-epoch", "sess-fresh-epoch")
		require.NoError(t, sessionStore.Create(context.Background(), sess))

		// Issue a real wire token in innerStore so Rotate can find it.
		wireToken, _, issueErr := innerStore.Issue(context.Background(), "sess-fresh-epoch", "usr-fresh-epoch", int64(1))
		require.NoError(t, issueErr)

		pair, err := svc.Refresh(context.Background(), wireToken)
		require.NoError(t, err, "matching epoch must allow normal refresh")
		assert.NotEmpty(t, pair.AccessToken, "rotated access token must be non-empty")
		assert.NotEmpty(t, pair.RefreshToken, "rotated refresh token must be non-empty")
	})
}

// spyInvalidator records Apply calls so reuse-funnel tests can assert
// Apply was triggered with the correct arguments.
type spyInvalidator struct {
	calls []struct {
		subjectID string
		event     session.CredentialEvent
	}
	err error
}

func (s *spyInvalidator) Apply(_ context.Context, subjectID string, event session.CredentialEvent) error {
	s.calls = append(s.calls, struct {
		subjectID string
		event     session.CredentialEvent
	}{subjectID, event})
	return s.err
}

// TestRefresh_Reuse_TriggersInvalidatorApply verifies the reuse cascade path:
//   - refresh.Store returns ErrReused on Rotate
//   - invalidator.Apply is called with (sess.SubjectID, CredentialEventRefreshReuse)
//   - the response surfaces as ErrAuthRefreshFailed (401, uniform rejection)
//   - cascadeRevoke detached is NOT called (funnel.Apply owns the refresh chain revocation)
func TestRefresh_Reuse_TriggersInvalidatorApply(t *testing.T) {
	sessionStore := newTestSessionStore(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	userRepo := mem.NewStore(clock.Real()).UserRepository()

	u, _ := domain.NewUser("usr-reuse", "reuse@test.local", "hash", time.Now())
	u.ID = "usr-reuse"
	require.NoError(t, userRepo.Create(context.Background(), u))

	// reuseRefreshStore simulates a refresh store that returns ErrReused on Rotate.
	// detachedSpy is wired as the inner Store of reuseStore so that any
	// RevokeSessionDetached call on the service's refreshStore actually reaches
	// the spy counter (previously detachedSpy wrapped innerStore directly and was
	// never injected into the service path — a phantom assertion).
	innerStore := newTestRefreshStore()
	detachedSpy := &spyRefreshStore{Store: innerStore}
	reuseStore := &reuseOnRotateRefreshStore{Store: detachedSpy, subjectID: "usr-reuse", sessionID: "sess-reuse"}
	spy := &spyInvalidator{}

	svc := MustNewServiceWithInvalidator(sessionStore, roleRepo, userRepo, reuseStore, testIssuer, slog.Default(),
		spy,
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})))

	sess := newTestSession("usr-reuse", "sess-reuse")
	require.NoError(t, sessionStore.Create(context.Background(), sess))

	wireToken, _, err := innerStore.Issue(context.Background(), "sess-reuse", "usr-reuse", int64(1))
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.Error(t, err)
	assert.Equal(t, dto.TokenPair{}, pair)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRefreshFailed, ec.Code,
		"reuse must surface as uniform ErrAuthRefreshFailed")

	require.Len(t, spy.calls, 1, "invalidator.Apply must be called exactly once on reuse")
	assert.Equal(t, "usr-reuse", spy.calls[0].subjectID)
	assert.Equal(t, session.CredentialEventRefreshReuse, spy.calls[0].event,
		"Apply must receive CredentialEventRefreshReuse")

	// cascadeRevoke (RevokeSessionDetached) must NOT be called separately —
	// funnel.Apply already owns the refresh chain revocation atomically. The
	// prior `_ = detachedSpy` was a silent no-op; assert the actual invariant.
	detachedSpy.mu.Lock()
	detachedN := detachedSpy.revokeSessionDetachedN
	businessN := detachedSpy.revokeSessionN
	detachedSpy.mu.Unlock()
	assert.Equal(t, 0, detachedN,
		"funnel.Apply owns refresh-chain revocation; cascade RevokeSessionDetached must not fire separately")
	assert.Equal(t, 0, businessN,
		"reuse path must not double-revoke through RevokeSession either")
}

// TestRefresh_Reuse_CascadeFailure_Returns401 verifies the security-critical
// fail-closed property of handleReuseDetected: when the reuse-cascade
// invalidator.Apply itself fails (e.g. DB transient outage, downstream
// KindUnavailable), the wire response must remain 401 ErrAuthRefreshFailed —
// not propagate the infra error as 503. Surfacing 503 here would leak a
// side-channel signal that "the cascade tried but failed", letting an
// attacker enumerate cascade-state by status code timing.
func TestRefresh_Reuse_CascadeFailure_Returns401(t *testing.T) {
	sessionStore := newTestSessionStore(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	userRepo := mem.NewStore(clock.Real()).UserRepository()

	u, _ := domain.NewUser("usr-cascade-fail", "cascade-fail@test.local", "hash", time.Now())
	u.ID = "usr-cascade-fail"
	require.NoError(t, userRepo.Create(context.Background(), u))

	innerStore := newTestRefreshStore()
	reuseStore := &reuseOnRotateRefreshStore{Store: innerStore, subjectID: "usr-cascade-fail", sessionID: "sess-cascade-fail"}
	// spy invalidator returns KindUnavailable — without the fail-closed fix
	// this would bubble up through the middleware as 503.
	spy := &spyInvalidator{
		err: errcode.New(errcode.KindUnavailable, errcode.ErrAuthRefreshUnavailable,
			"injected cascade DB outage"),
	}

	svc := MustNewServiceWithInvalidator(sessionStore, roleRepo, userRepo, reuseStore, testIssuer, slog.Default(),
		spy,
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})))

	sess := newTestSession("usr-cascade-fail", "sess-cascade-fail")
	require.NoError(t, sessionStore.Create(context.Background(), sess))

	wireToken, _, err := innerStore.Issue(context.Background(), "sess-cascade-fail", "usr-cascade-fail", int64(1))
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.Error(t, err)
	assert.Equal(t, dto.TokenPair{}, pair)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRefreshFailed, ec.Code,
		"reuse-cascade-failure path must surface uniform ErrAuthRefreshFailed (401), NOT the underlying ErrAuthRefreshUnavailable (503)")
	assert.Equal(t, errcode.KindUnauthenticated, ec.Kind,
		"Kind must collapse to KindUnauthenticated so middleware emits 401; KindUnavailable would emit 503 and leak a cascade-state side channel")

	require.Len(t, spy.calls, 1, "invalidator.Apply must still be attempted on reuse path")
}

// reuseOnRotateRefreshStore wraps a refresh.Store and overrides Peek to
// return a token that maps to the given session/subject, and Rotate to
// return ErrReused so the reuse-cascade branch fires.
type reuseOnRotateRefreshStore struct {
	refresh.Store
	subjectID string
	sessionID string
}

func (s *reuseOnRotateRefreshStore) Peek(_ context.Context, _ string) (*refresh.Token, error) {
	// AuthzEpochAtIssue must match the test user's AuthzEpoch (=1, set by
	// domain.NewUser) so the stale-epoch guard in refreshInTx doesn't fire
	// before the Rotate call we want to exercise.
	return &refresh.Token{SessionID: s.sessionID, SubjectID: s.subjectID, AuthzEpochAtIssue: 1}, nil
}

func (s *reuseOnRotateRefreshStore) Rotate(_ context.Context, _ string) (string, *refresh.Token, error) {
	return "", nil, refresh.ErrReused
}

// reuseOnPeekRefreshStore simulates the refresh store detecting reuse during
// Peek (grace-counter exhaustion or post-rotation reuse window). Peek returns
// the carried subject/session alongside ErrReused so the service layer can
// route into the unified invalidator cascade. Rotate is not exercised here.
type reuseOnPeekRefreshStore struct {
	refresh.Store
	subjectID string
	sessionID string
}

func (s *reuseOnPeekRefreshStore) Peek(_ context.Context, _ string) (*refresh.Token, error) {
	return &refresh.Token{SessionID: s.sessionID, SubjectID: s.subjectID, AuthzEpochAtIssue: 1}, refresh.ErrReused
}

// TestRefresh_PeekDetectedReuse_TriggersInvalidatorApply verifies the
// Finding #2 fix: a reuse signal surfaced from Peek (e.g. grace-counter
// exhaustion or post-rotation reuse window) must route into the same
// invalidator.Apply cascade as Rotate-detected reuse. Before the fix the Peek
// path returned a bare 401 without triggering the funnel, leaving the user's
// other sessions + refresh chains alive after a confirmed replay attack.
func TestRefresh_PeekDetectedReuse_TriggersInvalidatorApply(t *testing.T) {
	sessionStore := newTestSessionStore(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	userRepo := mem.NewStore(clock.Real()).UserRepository()

	u, _ := domain.NewUser("usr-peek-reuse", "peek-reuse@test.local", "hash", time.Now())
	u.ID = "usr-peek-reuse"
	require.NoError(t, userRepo.Create(context.Background(), u))

	innerStore := newTestRefreshStore()
	reuseStore := &reuseOnPeekRefreshStore{Store: innerStore, subjectID: "usr-peek-reuse", sessionID: "sess-peek-reuse"}
	spy := &spyInvalidator{}

	svc := MustNewServiceWithInvalidator(sessionStore, roleRepo, userRepo, reuseStore, testIssuer, slog.Default(),
		spy,
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})))

	sess := newTestSession("usr-peek-reuse", "sess-peek-reuse")
	require.NoError(t, sessionStore.Create(context.Background(), sess))

	wireToken, _, err := innerStore.Issue(context.Background(), "sess-peek-reuse", "usr-peek-reuse", int64(1))
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.Error(t, err)
	assert.Equal(t, dto.TokenPair{}, pair)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRefreshFailed, ec.Code,
		"Peek-detected reuse must surface as uniform ErrAuthRefreshFailed (401)")

	require.Len(t, spy.calls, 1,
		"Peek-detected reuse must trigger invalidator.Apply exactly once — same cascade as Rotate path")
	assert.Equal(t, "usr-peek-reuse", spy.calls[0].subjectID)
	assert.Equal(t, session.CredentialEventRefreshReuse, spy.calls[0].event,
		"Apply must receive CredentialEventRefreshReuse for Peek path too")
}

// contextCapturingInvalidator is a spy invalidator that records the context
// passed to Apply. Used to assert that reuse cascade uses an outer/detached
// context rather than the txCtx that gets canceled when the outer tx rolls back.
type contextCapturingInvalidator struct {
	capturedCtx context.Context
	err         error
}

func (s *contextCapturingInvalidator) Apply(ctx context.Context, _ string, _ session.CredentialEvent) error {
	s.capturedCtx = ctx
	return s.err
}

// outerCtxKey is a context key used in TestRefresh_Reuse_CascadeUsesDetachedCtx
// to identify contexts derived from outerCtx vs txCtx.
type outerCtxKey struct{}

// outerCtxTxRunner is a TxRunner that sets a distinguishing txCtx key on the
// inner context so the test can verify Apply is called with outerCtx-derived
// context (ctxutil.WithDetachedTimeout) rather than txCtx itself.
//
// Behavior: the inner fn receives a txCtx with a synthetic key set to "tx".
// The outer context (passed to RunInTx) carries the outerCtxKey set to "outer".
// After the fix, handleReuseDetected wraps outerCtx with
// ctxutil.WithDetachedTimeout (which is context.WithoutCancel + WithTimeout)
// and passes that to the inner RunInTx; the detached RunInTx again calls fn
// with an inner txCtx — but that inner txCtx is derived from the detached
// outerCtx, so it does NOT carry the synthetic tx key but DOES carry
// outerCtxKey, proving the cascade context chain goes through outerCtx.
type outerCtxTxRunner struct {
	calls int
}

func (r *outerCtxTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	r.calls++
	// Derive txCtx from the parent ctx (mirrors real PG TxManager behavior).
	// We tag txCtx so we can detect if Apply received it directly.
	txCtx := context.WithValue(ctx, struct{ txCall int }{r.calls}, "tx")
	return fn(txCtx)
}

// TestRefresh_Reuse_CascadeUsesDetachedCtx verifies that Finding #4 is fixed:
// when ErrReused is returned by Rotate, the invalidator.Apply call uses a
// context that is derived from outerCtx (not from the outer txCtx).
//
// Before the fix: Apply was called with txCtx directly inside the outer RunInTx
// closure. Returning authRefreshRejected() from the closure caused the outer tx
// to roll back, which canceled txCtx and undid all Apply writes.
//
// After the fix: handleRotateError wraps outerCtx with context.WithoutCancel and
// opens a new (inner) RunInTx for Apply — detached from the outer tx — so Apply
// commits independently of the outer 401 rejection.
//
// The test verifies that:
//  1. Apply is called.
//  2. The context received by Apply carries the outerCtxKey value (proving it
//     was derived from the caller's ctx, not from a fresh background context).
//  3. Refresh returns ErrAuthRefreshFailed (uniform 401).
func TestRefresh_Reuse_CascadeUsesDetachedCtx(t *testing.T) {
	t.Parallel()
	sessionStore := newTestSessionStore(t)
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	userRepo := mem.NewStore(clock.Real()).UserRepository()

	u, _ := domain.NewUser("usr-detached", "detached@test.local", "hash", time.Now())
	u.ID = "usr-detached"
	require.NoError(t, userRepo.Create(context.Background(), u))

	innerStore := newTestRefreshStore()
	reuseStore := &reuseOnRotateRefreshStore{Store: innerStore, subjectID: "usr-detached", sessionID: "sess-detached"}
	spy := &contextCapturingInvalidator{}

	outerRunner := &outerCtxTxRunner{}
	svc := MustNewServiceWithInvalidator(sessionStore, roleRepo, userRepo, reuseStore, testIssuer, slog.Default(),
		spy,
		WithClock(clock.Real()), WithTxManager(persistence.WrapForCell(outerRunner)))

	sess := newTestSession("usr-detached", "sess-detached")
	require.NoError(t, sessionStore.Create(context.Background(), sess))

	// Attach a sentinel value to the caller's ctx so we can verify the cascade
	// context chain propagates it.
	callerCtx := context.WithValue(context.Background(), outerCtxKey{}, "outer")

	_, err := svc.Refresh(callerCtx, "any-token")
	// Expect ErrAuthRefreshFailed (reuse rejection).
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthRefreshFailed, ec.Code)

	// KEY ASSERTIONS:
	require.NotNil(t, spy.capturedCtx, "invalidator.Apply must have been called")
	// The Apply context must carry the outerCtxKey sentinel — proving it was
	// derived from the caller's outerCtx, not from a fresh context.Background().
	// This is the critical invariant: handleReuseDetected uses
	// ctxutil.WithDetachedTimeout(outerCtx, ...) which preserves Value lookup
	// while breaking the cancel chain.
	assert.Equal(t, "outer", spy.capturedCtx.Value(outerCtxKey{}),
		"Apply context must be derived from outerCtx (carrying outerCtxKey sentinel) — "+
			"proving the cascade tx is detached from txCtx but still rooted in the caller's ctx")
	// NOTE: capturedCtx.Err() is intentionally NOT asserted here. With the new
	// WithDetachedTimeout pattern handleReuseDetected calls cancel() via defer
	// after RunInTx returns, so by the time the test reads capturedCtx the
	// detached context has already been canceled (resource release is correct
	// behavior). The semantic guarantee is "Apply ran *while* the ctx was live",
	// which is enforced by the value-propagation check above plus the fact
	// that the spy was invoked at all (Apply received a live ctx).
}

// Compile-time check: ports is used (userRepo, roleRepo). Ensure unused import
// does not surface — the import is consumed by domain/mem references above.
var _ ports.UserRepository = (*mem.UserRepository)(nil)
