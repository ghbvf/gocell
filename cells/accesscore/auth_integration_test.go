//go:build integration

// AUTH-INT-REACHABILITY-01 + PR-P0-AUTH-INTENT cell-level integration tests.
//
// These tests exercise the end-to-end composition of session-login (to mint a
// real access/refresh pair), session-validate (access-only verifier used by
// middleware), and session-refresh (refresh-only verifier) so that:
//
//  1. a legitimate access token actually reaches the verifier's "allow" path
//     (reachability — previously only negative-path coverage existed),
//  2. the public login handler returns a precisely-shaped 201 response, and
//  3. cross-intent replay is rejected by the appropriate verifier:
//     refresh→business=reject, access→refresh=reject.
package accesscore

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialinvalidate"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/testutil"
	"github.com/ghbvf/gocell/cells/accesscore/slices/rbacassign"
	"github.com/ghbvf/gocell/cells/accesscore/slices/sessionlogin"
	"github.com/ghbvf/gocell/cells/accesscore/slices/sessionlogout"
	"github.com/ghbvf/gocell/cells/accesscore/slices/sessionrefresh"
	"github.com/ghbvf/gocell/cells/accesscore/slices/sessionvalidate"
	"github.com/ghbvf/gocell/cells/internal/testoutbox"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
	"github.com/ghbvf/gocell/runtime/auth/session"
	"github.com/ghbvf/gocell/runtime/http/router"
)

// seedAdminPasswordHash caches the bcrypt hash for the seed admin password
// across every loginAndGetPair invocation. bcrypt at production cost is the
// dominant per-call cost in the integration suite (~hundreds of ms); a cached
// hash collapses N-case parallel runs to one hash computation.
var seedAdminPasswordHash = sync.OnceValue(func() string {
	hash, err := bcrypt.GenerateFromPassword([]byte(testPassword), domain.BcryptCost)
	if err != nil {
		panic(err)
	}
	return string(hash)
})

// loginConfig holds per-test audience configuration for loginAndGetPair.
type loginConfig struct {
	issuerAudsSet bool     // true = caller explicitly set; false = use default ["gocell"]
	issuerAuds    []string // empty = no WithIssuerAudiencesFromSlice option (issuer mints aud-less tokens)
	verifierAuds  []string // default ["gocell"]; empty intentionally panics in Verifier construction
}

type loginOption func(*loginConfig)

// withIssuerAuds sets the audiences the issuer embeds in tokens.
// Calling withIssuerAuds() with no arguments sets issuerAudsSet=true with an empty
// slice (no aud option — issuer mints aud-less tokens).
func withIssuerAuds(auds ...string) loginOption {
	return func(c *loginConfig) {
		c.issuerAudsSet = true
		c.issuerAuds = append([]string(nil), auds...)
	}
}

// withVerifierAuds sets the expected audiences the verifier will enforce.
func withVerifierAuds(auds ...string) loginOption {
	return func(c *loginConfig) {
		c.verifierAuds = append([]string(nil), auds...)
	}
}

// loginResult holds the output of a successful loginAndGetPair call.
type loginResult struct {
	AccessToken  string
	RefreshToken string
	Router       *router.Router
	Cell         *AccessCore
	Issuer       *auth.JWTIssuer
	Verifier     *auth.JWTVerifier
	Clock        *storetest.FakeClock
}

// loginAndGetPair pre-fills a user directly into repos, calls the real
// login HTTP handler through the initialized router, and returns the issued
// token pair on a precise 201 response. Failing this helper means the public
// login handler's status code or envelope drifted from the contract.
func loginAndGetPair(t *testing.T, opts ...loginOption) loginResult {
	t.Helper()

	cfg := loginConfig{verifierAuds: []string{"gocell"}}
	for _, o := range opts {
		o(&cfg)
	}

	userRepo := mem.NewStore(clock.Real()).UserRepository()
	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	ctx := context.Background()

	// Pre-fill alice as admin via direct repo seeding (no bootstrap flow).
	alice, err := domain.NewUser("alice", "alice@gocell.local", seedAdminPasswordHash(), time.Now())
	require.NoError(t, err)
	alice.ID = "usr-alice-integration"
	require.NoError(t, roleRepo.Create(ctx, &domain.Role{
		ID: auth.RoleAdmin, Name: auth.RoleAdmin,
		Permissions: []domain.Permission{{Resource: "*", Action: "*"}},
	}))
	require.NoError(t, userRepo.Create(ctx, alice))
	_, err = roleRepo.AssignToUser(ctx, alice.ID, auth.RoleAdmin)
	require.NoError(t, err)

	intClock := storetest.NewFakeClock(time.Now())
	intRefreshStore, err := refreshmem.New(refresh.Policy{
		ReuseInterval:  testtime.D2s,
		MaxAge:         time.Hour,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}, intClock, nil)
	require.NoError(t, err)

	ks, _, _ := auth.MustNewTestKeySet(clock.Real())

	require.NotEmpty(t, cfg.verifierAuds, "loginAndGetPair: verifierAuds must not be empty; use withVerifierAuds(\"gocell\") or similar")

	var issuerOpts []auth.JWTIssuerOption
	switch {
	case !cfg.issuerAudsSet:
		issuerOpts = append(issuerOpts, auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	case len(cfg.issuerAuds) > 0:
		issuerOpts = append(issuerOpts, auth.WithIssuerAudiencesFromSlice(cfg.issuerAuds))
		// else: explicitly empty → no audience option, issuer mints aud-less tokens.
	}
	issuer, err := auth.NewJWTIssuer(ks, "gocell", time.Hour, clock.Real(), issuerOpts...)
	require.NoError(t, err)

	verifier, err := auth.NewJWTVerifier(ks, clock.Real(), auth.WithExpectedAudiences(cfg.verifierAuds[0], cfg.verifierAuds[1:]...))
	require.NoError(t, err)

	c := NewAccessCore(
		WithClock(clock.Real()),
		WithUserRepository(userRepo),
		WithSessionStore(testutil.RealSessionRepo(t)),
		WithRoleRepository(roleRepo),
		WithOutboxDeps(outbox.WrapPublisherForCell(noopPublisher{}), nil),
		WithJWTIssuer(issuer),
		WithJWTVerifier(verifier),
		WithRefreshStore(intRefreshStore),
		WithMetricsProvider(metrics.NopProvider{}),
		// Demo mode: no tx+outbox required.
		withTestCASProtocol(),
		withTestBootstrapAuth(),
	)
	intReg := cell.NewRegistryRecorder(make(map[string]any), cell.DurabilityDemo)
	require.NoError(t, c.Init(context.Background(), intReg))

	intSnap := intReg.Snapshot()
	r := router.MustNew(router.WithRouterClock(clock.Real()))
	for _, rg := range intSnap.RouteGroups {
		rg := rg
		if rg.Listener == cell.PrimaryListener {
			if rg.Prefix != "" {
				r.Route(rg.Prefix, func(sub cell.RouteMux) { rg.Register(sub) })
			} else {
				rg.Register(r)
			}
		}
	}
	require.NoError(t, r.FinalizeAuth())

	body := strings.NewReader(`{"username":"alice","password":"` + testPassword + `"}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/access/sessions/login", body)
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	require.Equal(t, http.StatusCreated, rec.Code,
		"AUTH-INT-REACHABILITY-01: public login handler must return precisely 201, got %d body=%s",
		rec.Code, rec.Body.String())

	var envelope struct {
		Data struct {
			AccessToken  string `json:"accessToken"`
			RefreshToken string `json:"refreshToken"`
			ExpiresAt    string `json:"expiresAt"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope))
	require.NotEmpty(t, envelope.Data.AccessToken, "login response must include accessToken")
	require.NotEmpty(t, envelope.Data.RefreshToken, "login response must include refreshToken")
	require.NotEmpty(t, envelope.Data.ExpiresAt, "login response must include expiresAt")

	return loginResult{
		AccessToken:  envelope.Data.AccessToken,
		RefreshToken: envelope.Data.RefreshToken,
		Router:       r,
		Cell:         c,
		Issuer:       issuer,
		Verifier:     verifier,
		Clock:        intClock,
	}
}

func TestAuthIntent_AccessTokenReachesBusinessPath(t *testing.T) {
	fx := loginAndGetPair(t)

	// session-validate is wired with the JWT verifier inside cell.go. We call
	// it directly to mirror how AuthMiddleware would consult it.
	validateSvc, ok := fx.Cell.TokenVerifier().(*sessionvalidate.Service)
	require.True(t, ok, "TokenVerifier must be *sessionvalidate.Service in production wiring")

	claims, err := validateSvc.VerifyIntent(context.Background(), fx.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err, "legitimate access token must pass session-validate")
	assert.NotEmpty(t, claims.Subject)
}

func TestAuthIntent_RefreshTokenBlockedAtBusinessPath(t *testing.T) {
	fx := loginAndGetPair(t)

	validateSvc, ok := fx.Cell.TokenVerifier().(*sessionvalidate.Service)
	require.True(t, ok)

	_, err := validateSvc.VerifyIntent(context.Background(), fx.RefreshToken, auth.TokenIntentAccess)
	require.Error(t, err,
		"refresh token must NOT be accepted by session-validate (token confusion defense)")
}

func TestAuthIntent_AccessTokenBlockedAtRefreshPath(t *testing.T) {
	fx := loginAndGetPair(t)

	// Build a refresh-service that mirrors production wiring.
	// After the opaque-store rewrite, ParseOpaque rejects the JWT (wrong
	// selector/verifier format) → refresh.ErrRejected → ErrAuthRefreshFailed.
	refreshSvc := sessionrefresh.MustNewService(
		fx.Cell.sessionStore, fx.Cell.roleRepo, fx.Cell.userRepo, fx.Cell.refreshStore, fx.Cell.jwtIssuer, slog.Default(),
		sessionrefresh.WithClock(clock.Real()),
		sessionrefresh.WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
		sessionrefresh.WithInvalidator(credentialinvalidate.MustNew(fx.Cell.userRepo, fx.Cell.sessionStore, fx.Cell.refreshStore)),
	)

	_, err := refreshSvc.Refresh(context.Background(), fx.AccessToken)
	require.Error(t, err,
		"access token must NOT be accepted by session-refresh (token confusion defense)")
	var ec *errcode.Error
	require.True(t, errors.As(err, &ec), "refresh error must wrap *errcode.Error")
	assert.Equal(t, errcode.ErrAuthRefreshFailed, ec.Code,
		"intent mismatch collapses into ErrAuthRefreshFailed (enumeration defense)")
}

func TestAuthIntent_RefreshTokenSucceedsAtRefreshPath(t *testing.T) {
	fx := loginAndGetPair(t)

	// Need sessionlogin's persisted session (loginAndGetPair went through the
	// real login flow, so fx.Cell.sessionStore already has one).
	require.NotNil(t, fx.Cell.sessionStore, "session repo must be wired")

	refreshSvc := sessionrefresh.MustNewService(
		fx.Cell.sessionStore, fx.Cell.roleRepo, fx.Cell.userRepo, fx.Cell.refreshStore, fx.Cell.jwtIssuer, slog.Default(),
		sessionrefresh.WithClock(clock.Real()),
		sessionrefresh.WithTxManager(persistence.WrapForCell(cell.DemoTxRunner{})),
		sessionrefresh.WithInvalidator(credentialinvalidate.MustNew(fx.Cell.userRepo, fx.Cell.sessionStore, fx.Cell.refreshStore)),
	)

	newPair, err := refreshSvc.Refresh(context.Background(), fx.RefreshToken)
	require.NoError(t, err, "legitimate refresh token must rotate successfully")
	assert.NotEmpty(t, newPair.AccessToken)
	assert.NotEmpty(t, newPair.RefreshToken)
	accessClaims, err := fx.Verifier.VerifyIntent(context.Background(), newPair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err, "rotated access token must carry intent=access")
	assert.NotEmpty(t, accessClaims.Subject)
}

// TestAuthIntegration_RoleRevokeInvalidatesSession exercises the full transactional
// outbox path at the service layer:
//
//  1. Seed admin role + two users (bob with session, admin with a second admin role holder).
//  2. Revoke bob's "member" role via rbacassign.Service in durable mode
//     (stubOutboxWriter wrapped as an emitter, plus stubTxRunner).
//  3. Deliver the outbox entry synchronously to the sessionlogout consumer.
//  4. Assert that bob's session is now revoked.
//
// This is a slice-layer integration test (not HTTP) because the HTTP round-trip adds
// noise without testing the outbox→consumer wiring. The test runs the full service
// composition to mirror cell.Init wiring without the HTTP router overhead.
//
// NOTE: The EventRouter / ConsumerBase dispatch path is tested by the kernel/outbox
// and runtime/eventbus packages. Here we test the application-layer contract: that
// rbacassign produces the right outbox entry and the consumer handles it correctly.
func TestAuthIntegration_RoleRevokeInvalidatesSession(t *testing.T) {
	ctx := context.Background()

	// Shared repos (simulates cell's single repo wiring).
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	roleRepo := store.RoleRepository()
	sessionRepo := testutil.RealSessionRepo(t)
	refreshStore := testutil.RealRefreshStore(t)

	// Seed "member" role.
	roleRepo.SeedRole(&domain.Role{ID: "member", Name: "member"})
	// Seed "admin" role so bob doesn't become the last admin.
	roleRepo.SeedRole(&domain.Role{ID: "admin", Name: "admin"})

	// Seed bob so userRepo.BumpAuthzEpoch (called via invalidator funnel)
	// can find the row.
	bobNow := time.Now().UTC()
	bobUser, bobErr := domain.ReconstituteUser(
		"usr-bob", "bob", "bob@gocell.local", "x",
		1, false, domain.StatusActive, domain.UserSourceIdentity,
		1, bobNow, bobNow,
	)
	require.NoError(t, bobErr)
	require.NoError(t, userRepo.Create(ctx, bobUser))

	// Assign bob and carol to "member" so last-holder guard doesn't block.
	_, _ = roleRepo.AssignToUser(ctx, "usr-bob", "member")
	_, _ = roleRepo.AssignToUser(ctx, "usr-carol", "member")

	// Give bob an active session.
	bobSession := &session.Session{
		ID:                "sess-bob",
		SubjectID:         "usr-bob",
		JTI:               "sess-bob",
		AuthzEpochAtIssue: 1,
		CreatedAt:         time.Now().UTC(),
		ExpiresAt:         time.Now().Add(time.Hour).UTC(),
	}
	require.NoError(t, sessionRepo.Create(ctx, bobSession))

	// S4b: rbacassign.Revoke now drives credential invalidation through the
	// credentialinvalidate funnel (3-op same-tx: bump epoch + revoke sessions
	// + revoke refresh chain). The sessionlogout consumer is audit/ack-only.
	invalidator := credentialinvalidate.MustNew(userRepo, sessionRepo, refreshStore)

	// Wire rbacassign with outbox stubs (durable mode).
	stubWriter := &rbacStubOutboxWriter{}
	stubTx := &rbacStubTxRunner{}
	assignSvc, err := rbacassign.NewService(
		roleRepo, invalidator, slog.Default(),
		rbacassign.WithEmitter(testoutbox.MustEmitter(t, stubWriter)),
		rbacassign.WithTxManager(persistence.WrapForCell(stubTx)),
	)
	require.NoError(t, err)

	// Wire the sessionlogout consumer (audit/ack-only after S4b funnel refactor).
	consumer := sessionlogout.NewConsumer(slog.Default())

	// Revoke bob's member role — should produce one outbox entry.
	require.NoError(t, assignSvc.Revoke(ctx, "usr-bob", "member"))
	require.Len(t, stubWriter.entries, 1, "Revoke must produce exactly one outbox entry")

	// Deliver the outbox entry synchronously to the consumer (simulates relay dispatch).
	res := consumer.HandleRoleChanged(ctx, stubWriter.entries[0])
	require.Equal(t, outbox.DispositionAck, res.Disposition,
		"role-revoke event must be Acked; got %v err=%v", res.Disposition, res.Err)
	assert.NoError(t, res.Err, "role-revoke Ack must carry nil Err")

	// Bob's session must now be revoked.
	sess, err := sessionRepo.Get(ctx, "sess-bob")
	require.NoError(t, err)
	assert.True(t, sess.RevokedAt != nil,
		"session must be revoked after role-revoke outbox entry is consumed")
}

// TestAuthIntegration_LoginAccessTokenAudienceDrift verifies that audience
// mismatches between issuer and verifier are correctly detected and rejected.
func TestAuthIntegration_LoginAccessTokenAudienceDrift(t *testing.T) {
	cases := []struct {
		name         string
		issuerAuds   []string
		verifierAuds []string
		wantErrCode  errcode.Code // empty = expect verify success
	}{
		{"aligned_audiences_pass", []string{"gocell"}, []string{"gocell"}, ""},
		{"issuer_drift_rejected", []string{"gocell-other"}, []string{"gocell"}, errcode.ErrAuthInvalidTokenIntent},
		{"token_rejected_when_verifier_expects_other_aud", []string{"gocell"}, []string{"gocell-other"}, errcode.ErrAuthInvalidTokenIntent},
		{"multi_aud_one_match_pass", []string{"a", "gocell"}, []string{"gocell"}, ""},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			fx := loginAndGetPair(
				t,
				withIssuerAuds(tc.issuerAuds...),
				withVerifierAuds(tc.verifierAuds...),
			)
			_, err := fx.Verifier.VerifyIntent(t.Context(), fx.AccessToken, auth.TokenIntentAccess)
			if tc.wantErrCode == "" {
				require.NoError(t, err, "case %s: aligned audiences must pass verifier", tc.name)
				return
			}
			require.Error(t, err, "case %s: drift must be rejected", tc.name)
			var ec *errcode.Error
			require.True(t, errors.As(err, &ec), "case %s: error must wrap *errcode.Error", tc.name)
			assert.Equal(t, tc.wantErrCode, ec.Code, "case %s: error code", tc.name)
		})
	}
}

// rbacStubOutboxWriter captures entries for slice-layer integration tests.
// Defined here to avoid package-crossing (rbacassign is a different package).
type rbacStubOutboxWriter struct {
	entries []outbox.Entry
}

func (w *rbacStubOutboxWriter) Write(_ context.Context, e outbox.Entry) error {
	w.entries = append(w.entries, e)
	return nil
}

// rbacStubTxRunner executes fn directly (no real transaction), simulating in-memory behaviour.
type rbacStubTxRunner struct{}

func (rbacStubTxRunner) RunInTx(_ context.Context, fn func(context.Context) error) error {
	return fn(context.Background())
}

// Compile-time proof these tests hit the real slices (not stubs).
var (
	_ = (*sessionlogin.Service)(nil)
	_ = (*sessionrefresh.Service)(nil)
	_ = (*sessionvalidate.Service)(nil)
)
