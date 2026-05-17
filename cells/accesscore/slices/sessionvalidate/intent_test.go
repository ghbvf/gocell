// PR-P0-AUTH-INTENT: sessionvalidate.VerifyIntent must reject refresh-intent
// JWTs (token confusion) so that middleware never surfaces a refresh token's
// claims to a business handler.
package sessionvalidate

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/authtest"
)

func TestService_VerifyIntent_RejectsRefreshIntentToken(t *testing.T) {
	priv, pub := authtest.MustGenerateKeyPair()
	ks, err := auth.NewKeySet(priv, pub, clock.Real())
	require.NoError(t, err)
	verifier, err := auth.NewJWTVerifier(ks, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	// sessionStore nil = demo mode; userRepo required by ctor but never called.
	svc, err := NewService(verifier, nil, &stubUserRepo{}, slog.Default())
	require.NoError(t, err)

	refreshTok, err := IssueLegacyRefreshJWT(priv, "usr-attacker", time.Hour)
	require.NoError(t, err)

	_, err = svc.VerifyIntent(context.Background(), refreshTok, auth.TokenIntentAccess)
	require.Error(t, err, "refresh-intent token must be rejected by sessionvalidate")
	assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN",
		"intent mismatch maps to generic invalid-token code in response")
}

func TestService_VerifyIntent_AcceptsAccessIntentToken(t *testing.T) {
	priv, pub := authtest.MustGenerateKeyPair()
	ks, err := auth.NewKeySet(priv, pub, clock.Real())
	require.NoError(t, err)
	verifier, err := auth.NewJWTVerifier(ks, clock.Real(), auth.WithExpectedAudiences("gocell"))
	require.NoError(t, err)

	// sessionStore nil = demo mode; userRepo required by ctor but never called.
	svc, err := NewService(verifier, nil, &stubUserRepo{}, slog.Default())
	require.NoError(t, err)

	accessTok, err := IssueTestTokenWithIntent(priv, auth.TokenIntentAccess,
		"usr-legit", []string{"user"}, time.Hour)
	require.NoError(t, err)

	claims, err := svc.VerifyIntent(context.Background(), accessTok, auth.TokenIntentAccess)
	require.NoError(t, err)
	assert.Equal(t, "usr-legit", claims.Subject)
	assert.Equal(t, auth.TokenIntentAccess, claims.TokenUse)
}
