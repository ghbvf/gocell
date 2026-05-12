// PR-P0-AUTH-INTENT: verifies that session-refresh rejects access-intent
// tokens (token confusion attack) and that the token pair it mints carries
// the correct intents.
package sessionrefresh

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth"
)

// TestAuthIntent_AccessTokenBlockedAtRefreshPath verifies that passing an
// access JWT to the refresh endpoint returns ErrAuthRefreshFailed.
// After the opaque-store rewrite, ParseOpaque rejects the JWT (wrong
// selector/verifier format) → refresh.ErrRejected → ErrAuthRefreshFailed.
func TestService_Refresh_RejectsAccessIntentToken(t *testing.T) {
	svc, _ := newTestService(t)

	// Issue an ACCESS-intent JWT (wrong for /auth/refresh).
	bogusAccess, err := testIssuer.Issue(auth.TokenIntentAccess, "usr-att", auth.IssueOptions{
		Roles:     []string{"user"},
		Audience:  []string{"gocell"},
		SessionID: "sess-att",
	})
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), bogusAccess)
	require.Error(t, err, "access-intent token must not be accepted as a refresh token")
	assert.Empty(t, pair.AccessToken)
	assert.Contains(t, err.Error(), "ERR_AUTH_REFRESH_FAILED",
		"intent mismatch must collapse into the generic refresh-failed code (enumeration defense)")
}

func TestService_Refresh_NewTokensCarryCorrectIntents(t *testing.T) {
	svc, store, refreshStore := newTestServiceWithRefreshStore(t, "usr-r1")

	sess := newTestSession("usr-r1", "sess-r1")
	require.NoError(t, store.Create(context.Background(), sess))

	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-r1", "usr-r1")
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.NoError(t, err)
	require.NotEmpty(t, pair.AccessToken)

	verifier, err := auth.NewJWTVerifier(testKeySet, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	newAccess, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, auth.TokenIntentAccess, newAccess.TokenUse)

	// The new refresh token is an opaque wire token, not a JWT — it should NOT
	// verify as an access token.
	_, err = verifier.VerifyIntent(context.Background(), pair.RefreshToken, auth.TokenIntentAccess)
	require.Error(t, err, "opaque wire token must not verify as access JWT")
}
