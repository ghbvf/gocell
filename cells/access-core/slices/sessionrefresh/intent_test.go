// PR-P0-AUTH-INTENT: verifies that session-refresh rejects access-intent
// tokens (token confusion attack) and that the token pair it mints carries
// the correct intents.
package sessionrefresh

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_Refresh_RejectsAccessIntentToken(t *testing.T) {
	svc, repo := newTestService()

	// Issue an ACCESS-intent JWT (wrong for /auth/refresh) tied to a session.
	bogusAccess, err := testIssuer.Issue(auth.TokenIntentAccess, "usr-att", []string{"user"}, []string{"gocell"}, "sess-att")
	require.NoError(t, err)

	sess, err := domain.NewSession("usr-att", "at", bogusAccess, time.Now().Add(time.Hour))
	require.NoError(t, err)
	sess.ID = "sess-att"
	require.NoError(t, repo.Create(context.Background(), sess))

	pair, err := svc.Refresh(context.Background(), bogusAccess)
	require.Error(t, err, "access-intent token must not be accepted as a refresh token")
	assert.Nil(t, pair)
	assert.Contains(t, err.Error(), "ERR_AUTH_REFRESH_FAILED",
		"intent mismatch must collapse into the generic refresh-failed code (enumeration defense)")
}

func TestService_Refresh_NewTokensCarryCorrectIntents(t *testing.T) {
	svc, repo := newTestService()

	refresh := issueTestToken("usr-r1")
	sess, err := domain.NewSession("usr-r1", "access-tok", refresh, time.Now().Add(time.Hour))
	require.NoError(t, err)
	sess.ID = "sess-r1"
	require.NoError(t, repo.Create(context.Background(), sess))

	pair, err := svc.Refresh(context.Background(), refresh)
	require.NoError(t, err)
	require.NotNil(t, pair)

	verifier, err := auth.NewJWTVerifier(testKeySet, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	newAccess, err := verifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, auth.TokenIntentAccess, newAccess.TokenUse)

	newRefresh, err := verifier.VerifyIntent(context.Background(), pair.RefreshToken, auth.TokenIntentRefresh)
	require.NoError(t, err)
	assert.Equal(t, auth.TokenIntentRefresh, newRefresh.TokenUse)

	// The freshly rotated refresh token must NOT verify as access.
	_, err = verifier.VerifyIntent(context.Background(), pair.RefreshToken, auth.TokenIntentAccess)
	require.Error(t, err)
}

// mem import above is only used by domain.NewSession setup.
var _ = (*mem.SessionRepository)(nil)
