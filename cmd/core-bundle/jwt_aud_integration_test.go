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
// by buildJWTDeps rejects tokens whose aud claim does not contain the configured audience.
// This exercises the RFC 8725 §3.3 audience validation wiring end-to-end:
// GOCELL_JWT_AUDIENCE → NewJWTVerifier(WithExpectedAudiences) → VerifyIntent.
func TestBuildJWTDeps_VerifierEnforcesAudience(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "gocell-test")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")
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

	t.Run("rejects_explicitly_empty_audience", func(t *testing.T) {
		// Issue a token with an explicit wrong audience to test rejection.
		// (nil audience falls back to the Registry-configured default "gocell",
		// so we must supply an explicit wrong value instead.)
		tok, err := deps.issuer.Issue(auth.TokenIntentAccess, "user-1", auth.IssueOptions{
			Audience: []string{"not-gocell"},
		})
		require.NoError(t, err)

		_, err = deps.verifier.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
		require.Error(t, err, "token with wrong aud must be rejected when expected audience is configured")
		assert.Contains(t, err.Error(), "ERR_AUTH_INVALID_TOKEN_INTENT")
	})
}

// TestBuildJWTDeps_VerifierAudience_MatchesIssuerDefault verifies that the
// audience configured via GOCELL_JWT_AUDIENCE is written into issued tokens and
// accepted by the paired verifier, forming a self-consistent configuration.
// This test pins the contract so a future drift in env-var vs. issuer audience fails loudly.
func TestBuildJWTDeps_VerifierAudience_MatchesIssuerDefault(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "gocell-test")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")
	deps, err := buildJWTDeps("")
	require.NoError(t, err)

	// The audience configured via GOCELL_JWT_AUDIENCE is set as the issuer's default
	// (via Registry). Simulate what sessionlogin.Service.issueAccessToken does: rely on
	// the issuer's Registry-configured default audience.
	tok, err := deps.issuer.Issue(
		auth.TokenIntentAccess, "user-1", auth.IssueOptions{
			Roles:     []string{"admin"},
			SessionID: "sess-1",
			// Audience left nil — issuer uses Registry-configured default automatically.
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
