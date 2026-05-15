package sessionvalidate

// epoch_only_test.go isolates the two security predicates that
// enforceSessionState applies *after* the JWT signature + revocation checks:
//
//  1. user.AuthzEpoch != view.AuthzEpochAtIssue → 401 (S4d row-provenance, Finding #1)
//  2. view.SubjectID  != claims.Subject          → 401 (Finding #5 / PR #490)
//
// S4d change: epoch comparison source moved from JWT claim to session row.
// The session row's AuthzEpochAtIssue captures the user's epoch at login time.
// A credential event bumps user.AuthzEpoch; all sessions with the old epoch
// mismatch and are rejected.
//
// The existing TestS4b_CredentialEvent_InvalidatesAccessJWT integration test
// proves the bump + revoke funnel atomically does *both*, which means a
// regression that left epoch reads at 0 would still pass that test (session
// revocation alone yields 401, masking the epoch read bug). These tests
// hold the session row "active" (RevokedAt = nil, SubjectID = owner) and
// vary only the epoch / subject — so a regression that silently zeroes the
// read path or drops the subject-mismatch branch fails here loudly.

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// TestEnforceSessionState_EpochMismatch_RejectsWithoutSessionRevoke verifies
// that the epoch predicate alone rejects a stale token, even when the session
// row is still active. S4d: comparison is user.AuthzEpoch vs view.AuthzEpochAtIssue.
//
// Scenario: session created at user.epoch=1 (AuthzEpochAtIssue=1). Credential
// event bumps user.epoch to 2. The session row is still active (not revoked),
// but the epoch comparison rejects because 2 != 1.
//
// This guards the "PG SELECT silently drops authz_epoch column" regression —
// without epoch read, user.AuthzEpoch=0 and any session with AuthzEpochAtIssue=1
// would fail constantly (0 != 1), making the regression visible.
func TestEnforceSessionState_EpochMismatch_RejectsWithoutSessionRevoke(t *testing.T) {
	memStore := mem.NewStore(clock.Real())
	userRepo := memStore.UserRepository()
	sessionStore := newTestStore(t)

	userID := "usr-epoch-only-" + uuid.NewString()[:8]
	sessionID := "sess-epoch-only-" + uuid.NewString()[:8]
	// S4d: user starts at epoch=1 (domain.NewUser sets AuthzEpoch=1).
	user := mustBuildUser(t, userID, 1)
	user.Username = "epoch-only-user"
	user.Email = "epoch@only.test"
	require.NoError(t, userRepo.Create(context.Background(), user))

	// Seed an active session — AuthzEpochAtIssue=1 matches user.epoch=1.
	// RevokedAt deliberately stays nil so the epoch branch is the ONLY
	// rejection path after the bump.
	require.NoError(t, sessionStore.Create(context.Background(), &session.Session{
		ID:                sessionID,
		SubjectID:         userID,
		JTI:               "jti-epoch-" + sessionID,
		AuthzEpochAtIssue: 1,
		CreatedAt:         time.Now(),
		ExpiresAt:         time.Now().Add(time.Hour),
	}))

	svc, err := NewService(testVerifier, sessionStore, userRepo, slog.Default())
	require.NoError(t, err)

	// Token with active session: user.epoch=1 == session.AuthzEpochAtIssue=1 → ACCEPT.
	tok, err := IssueTestToken(testPrivKey, userID, nil, time.Hour, sessionID)
	require.NoError(t, err)
	_, err = svc.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.NoError(t, err, "before bump: user.epoch=1 matches session.epoch=1 → must accept")

	// Bump epoch via the repo directly — funnel intentionally bypassed so the
	// session row remains active and only the epoch predicate can reject.
	newEpoch, err := userRepo.BumpAuthzEpoch(context.Background(), userID)
	require.NoError(t, err)
	require.Equal(t, int64(2), newEpoch, "first bump must advance epoch from 1 to 2")

	// The same token must now be rejected: user.epoch=2 != session.epoch=1.
	// If the PG SELECT regression returns and user.AuthzEpoch reads as 0,
	// then 0 != 1 still fails — but for the wrong reason. The pre-bump ACCEPT
	// above proves the read path is working (otherwise 1 != 1 would have failed).
	_, err = svc.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.Error(t, err, "post-bump: user.epoch=2 != session.epoch=1 → must reject purely on epoch mismatch")

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.KindUnauthenticated, ec.Kind)
}

// TestEnforceSessionState_SubjectMismatch_Rejects exercises the defense-in-depth
// branch added in Finding #5: a live session whose SubjectID differs from the
// JWT sub must 401, blocking a signing-path regression that bound one
// subject's claims to another subject's session.
func TestEnforceSessionState_SubjectMismatch_Rejects(t *testing.T) {
	memStore := mem.NewStore(clock.Real())
	userRepo := memStore.UserRepository()
	sessionStore := newTestStore(t)

	// Two distinct subjects sharing nothing but the same session row id.
	ownerID := "usr-owner-" + uuid.NewString()[:8]
	imposterID := "usr-imposter-" + uuid.NewString()[:8]
	sessionID := "sess-mismatch-" + uuid.NewString()[:8]

	for _, id := range []string{ownerID, imposterID} {
		u := mustBuildUser(t, id, 1)
		u.Email = id + "@test"
		require.NoError(t, userRepo.Create(context.Background(), u))
	}

	// Seed an active session owned by ownerID. AuthzEpochAtIssue=1 matches ownerID.epoch=1.
	require.NoError(t, sessionStore.Create(context.Background(), &session.Session{
		ID:                sessionID,
		SubjectID:         ownerID,
		JTI:               "jti-mismatch-" + sessionID,
		AuthzEpochAtIssue: 1,
		CreatedAt:         time.Now(),
		ExpiresAt:         time.Now().Add(time.Hour),
	}))

	svc, err := NewService(testVerifier, sessionStore, userRepo, slog.Default())
	require.NoError(t, err)

	// Forge a token whose sub is imposter but sid points at owner's session.
	// In production this should never happen, but a signing-path regression
	// could produce this shape; the defense-in-depth check must reject it.
	imposterTok, err := IssueTestToken(testPrivKey, imposterID, nil, time.Hour, sessionID)
	require.NoError(t, err)
	_, err = svc.VerifyIntent(context.Background(), imposterTok, auth.TokenIntentAccess)
	require.Error(t, err,
		"sid pointing at a different subject's session must be rejected (Finding #5 defense-in-depth)")

	// Sanity: owner's own token still passes.
	ownerTok, err := IssueTestToken(testPrivKey, ownerID, nil, time.Hour, sessionID)
	require.NoError(t, err)
	_, err = svc.VerifyIntent(context.Background(), ownerTok, auth.TokenIntentAccess)
	assert.NoError(t, err, "owner's token against owner's session must verify")
}

// infraOnlyVerifier always returns a KindUnavailable errcode regardless of
// the token contents — simulating a downstream verifier whose key provider
// is unreachable. Used by TestVerifyIntent_VerifierInfra_Preserves503.
type infraOnlyVerifier struct{}

func (infraOnlyVerifier) VerifyIntent(_ context.Context, _ string, _ auth.TokenIntent) (auth.Claims, error) {
	return auth.Claims{}, errcode.New(errcode.KindUnavailable, errcode.ErrAuthServiceUnavailable,
		"jwks fetch failed",
		errcode.WithCategory(errcode.CategoryInfra))
}

// Compile-time check: infraOnlyVerifier satisfies auth.IntentTokenVerifier.
var _ auth.IntentTokenVerifier = infraOnlyVerifier{}

// TestVerifyIntent_VerifierInfra_Preserves503 guards Finding #2 PR #490
// second review: sessionvalidate.verifyJWTWithIntent previously wrapped every
// verifier error as ErrAuthInvalidToken (401), silently downgrading the
// underlying KindUnavailable to 401 and masking auth-dependency outages as
// credential failures. The fixed path must propagate KindUnavailable so the
// middleware layer can emit a 503.
func TestVerifyIntent_VerifierInfra_Preserves503(t *testing.T) {
	svc, err := NewService(infraOnlyVerifier{}, nil /*sessionStore*/, mem.NewStore(clock.Real()).UserRepository(), slog.Default())
	require.NoError(t, err)

	_, err = svc.VerifyIntent(context.Background(), "any-token", auth.TokenIntentAccess)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec,
		"verifier infra error must arrive as *errcode.Error, not a bare wrapped string")
	assert.Equal(t, errcode.KindUnavailable, ec.Kind,
		"verifier KindUnavailable must propagate unchanged through verifyJWTWithIntent; "+
			"downgrading to KindUnauthenticated masks an auth dependency outage as a credential failure (Finding #2)")
	assert.Equal(t, errcode.ErrAuthServiceUnavailable, ec.Code,
		"source code must be preserved so server logs / metrics can route the failure correctly")
}
