// PR-P0-AUTH-INTENT: sessionvalidate.Verify must reject refresh-intent JWTs
// (token confusion) so that middleware never surfaces a refresh token's
// claims to a business handler.
package sessionvalidate

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestService_Verify_RejectsRefreshIntentToken(t *testing.T) {
	priv, pub := auth.MustGenerateTestKeyPair()
	ks, err := auth.NewKeySet(priv, pub)
	require.NoError(t, err)
	verifier, err := auth.NewJWTVerifier(ks, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	svc := NewService(verifier, nil, slog.Default())

	refreshTok, err := IssueTestTokenWithIntent(priv, auth.TokenIntentRefresh,
		"usr-attacker", nil, time.Hour)
	require.NoError(t, err)

	_, err = svc.Verify(context.Background(), refreshTok)
	require.Error(t, err, "refresh-intent token must be rejected by sessionvalidate")
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN",
		"intent mismatch maps to generic invalid-token code in response")
}

func TestService_Verify_AcceptsAccessIntentToken(t *testing.T) {
	priv, pub := auth.MustGenerateTestKeyPair()
	ks, err := auth.NewKeySet(priv, pub)
	require.NoError(t, err)
	verifier, err := auth.NewJWTVerifier(ks, auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	svc := NewService(verifier, nil, slog.Default())

	accessTok, err := IssueTestTokenWithIntent(priv, auth.TokenIntentAccess,
		"usr-legit", []string{"user"}, time.Hour)
	require.NoError(t, err)

	claims, err := svc.Verify(context.Background(), accessTok)
	require.NoError(t, err)
	assert.Equal(t, "usr-legit", claims.Subject)
	assert.Equal(t, auth.TokenIntentAccess, claims.TokenUse)
}
