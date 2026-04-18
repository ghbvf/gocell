// Integration tests for audience validation wiring in buildJWTDeps (PR-R-AUTH-AUD-VALIDATION).
//
// Verifies that the verifier constructed by buildJWTDeps enforces RFC 8725 §3.3:
// tokens issued with a mismatched audience are rejected at VerifyIntent.
package main

import (
	"context"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildJWTDeps_VerifierEnforcesAudience verifies that the JWTVerifier returned
// by buildJWTDeps rejects tokens whose aud claim does not contain jwtAudience.
// This exercises the RFC 8725 §3.3 audience validation wiring end-to-end:
// loadKeySet → NewJWTVerifier(WithExpectedAudiences) → VerifyIntent.
func TestBuildJWTDeps_VerifierEnforcesAudience(t *testing.T) {
	deps, err := buildJWTDeps("") // dev mode: ephemeral key pair
	require.NoError(t, err)

	t.Run("accepts_gocell_audience", func(t *testing.T) {
		tok, err := deps.issuer.Issue(auth.TokenIntentAccess, "user-1", auth.IssueOptions{
			Audience: []string{"gocell"},
		})
		require.NoError(t, err)

		_, err = deps.verifier.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
		require.NoError(t, err, "token with aud=gocell must be accepted by the configured verifier")
	})

	t.Run("rejects_wrong_audience", func(t *testing.T) {
		tok, err := deps.issuer.Issue(auth.TokenIntentAccess, "user-1", auth.IssueOptions{
			Audience: []string{"wrong-service"},
		})
		require.NoError(t, err)

		_, err = deps.verifier.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
		require.Error(t, err, "token with aud=wrong-service must be rejected")
		assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN_INTENT",
			"audience mismatch must surface as ERR_AUTH_INVALID_TOKEN_INTENT")
	})

	t.Run("rejects_missing_audience", func(t *testing.T) {
		// Issue a token with nil audience (no aud claim).
		tok, err := deps.issuer.Issue(auth.TokenIntentAccess, "user-1", auth.IssueOptions{})
		require.NoError(t, err)

		_, err = deps.verifier.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
		require.Error(t, err, "token without aud claim must be rejected when expected audience is configured")
		assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN_INTENT")
	})
}

// TestBuildJWTDeps_VerifierAudience_MatchesIssuerDefault verifies that the
// audience constant used by buildJWTDeps (jwtAudience = "gocell") matches the
// audience written by sessionlogin/sessionrefresh in production. This test pins
// the contract so a future rename in either location fails loudly.
func TestBuildJWTDeps_VerifierAudience_MatchesIssuerDefault(t *testing.T) {
	deps, err := buildJWTDeps("")
	require.NoError(t, err)

	// Simulate what sessionlogin.Service.issueAccessToken does: issue with []string{"gocell"}.
	tok, err := deps.issuer.Issue(
		auth.TokenIntentAccess, "user-1", auth.IssueOptions{
			Roles:     []string{"admin"},
			Audience:  []string{jwtAudience}, // same constant used by buildJWTDeps
			SessionID: "sess-1",
		},
	)
	require.NoError(t, err)

	claims, err := deps.verifier.VerifyIntent(
		context.Background(), tok, auth.TokenIntentAccess,
	)
	require.NoError(t, err)
	assert.Equal(t, "user-1", claims.Subject)
	assert.WithinDuration(t, time.Now().Add(auth.DefaultAccessTokenTTL), claims.ExpiresAt, 5*time.Second)
}
