// PR-P0-AUTH-INTENT: verifies that session-refresh rejects access-intent
// tokens (token confusion attack) and that the token pair it mints carries
// the correct intents.
package sessionrefresh

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestAuthIntent_AccessTokenBlockedAtRefreshPath verifies that passing an
// access JWT to the refresh endpoint returns ErrAuthRefreshFailed.
// After the opaque-store rewrite, ParseOpaque rejects the JWT (wrong
// selector/verifier format) → refresh.ErrRejected → ErrAuthRefreshFailed.
func TestService_Refresh_RejectsAccessIntentToken(t *testing.T) {
	svc, _ := newTestService()

	// Issue an ACCESS-intent JWT (wrong for /auth/refresh).
	bogusAccess, err := testIssuer.Issue(auth.TokenIntentAccess, "usr-att", auth.IssueOptions{
		Roles:     []string{"user"},
		Audience:  []string{"gocell"},
		SessionID: "sess-att",
	})
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), bogusAccess)
	require.Error(t, err, "access-intent token must not be accepted as a refresh token")
	assert.Nil(t, pair)
	assert.Contains(t, err.Error(), "ERR_AUTH_REFRESH_FAILED",
		"intent mismatch must collapse into the generic refresh-failed code (enumeration defense)")
}

func TestService_Refresh_NewTokensCarryCorrectIntents(t *testing.T) {
	svc, repo, refreshStore := newTestServiceWithRefreshStore("usr-r1")

	sess, err := domain.NewSession("usr-r1", "access-tok", time.Now().Add(time.Hour))
	require.NoError(t, err)
	sess.ID = "sess-r1"
	require.NoError(t, repo.Create(context.Background(), sess))

	wireToken, _, err := refreshStore.Issue(context.Background(), "sess-r1", "usr-r1")
	require.NoError(t, err)

	pair, err := svc.Refresh(context.Background(), wireToken)
	require.NoError(t, err)
	require.NotNil(t, pair)

	verifier, err := auth.NewJWTVerifier(testKeySet, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	newAccess, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, auth.TokenIntentAccess, newAccess.TokenUse)

	// The new refresh token is an opaque wire token, not a JWT — it should NOT
	// verify as an access token.
	_, err = verifier.VerifyIntent(context.Background(), pair.RefreshToken, auth.TokenIntentAccess)
	require.Error(t, err, "opaque wire token must not verify as access JWT")
}

// mem import above is only used by domain.NewSession setup.
var _ = (*mem.SessionRepository)(nil)
