// PR-P0-AUTH-INTENT: verifies that session-login issues JWTs whose token_use
// claim and JOSE typ header match the expected TokenIntent for each token
// slot of the returned pair.
package sessionlogin

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/runtime/auth"
)

func TestService_Login_IssuesDistinctIntents(t *testing.T) {
	svc, userRepo := newTestService()
	seedUser(userRepo, "alice", "s3cret!")

	pair, err := svc.Login(context.Background(), LoginInput{Username: "alice", Password: "s3cret!"})
	require.NoError(t, err)
	require.NotNil(t, pair)

	testVerifier, err := auth.NewJWTVerifier(testKeySet, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	accessClaims, err := testVerifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntentAccess)
	require.NoError(t, err, "access token must verify as intent=access")
	assert.Equal(t, auth.TokenIntentAccess, accessClaims.TokenUse)

	// Refresh token is now an opaque wire token (not a JWT) — it must not verify as any JWT intent.
	_, err = testVerifier.VerifyIntent(context.Background(), pair.RefreshToken, auth.TokenIntentAccess)
	require.Error(t, err, "opaque refresh token must NOT verify as access JWT intent")

	_, err = testVerifier.VerifyIntent(context.Background(), pair.AccessToken, auth.TokenIntent("refresh"))
	require.Error(t, err, "access token must NOT verify as refresh intent")
}
