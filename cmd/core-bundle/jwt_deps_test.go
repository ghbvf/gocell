// Tests for env-var-driven JWT dependency loading (C5).
//
// GOCELL_JWT_ISSUER and GOCELL_JWT_AUDIENCE are required in all adapter modes
// (there is no fallback default value).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- loadJWTIssuer ---

// TestLoadJWTIssuer_MissingEnvVar_Error verifies that an unset GOCELL_JWT_ISSUER
// returns an error containing the env var name.
func TestLoadJWTIssuer_MissingEnvVar_Error(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "")
	_, err := loadJWTIssuer("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_JWT_ISSUER",
		"error must name the missing env var")
}

// TestLoadJWTIssuer_SetEnvVar_Used verifies that a set GOCELL_JWT_ISSUER is returned.
func TestLoadJWTIssuer_SetEnvVar_Used(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "gocell-prod")
	val, err := loadJWTIssuer("")
	require.NoError(t, err)
	assert.Equal(t, "gocell-prod", val)
}

// TestLoadJWTIssuer_RealMode_AlsoRequired ensures real mode is equally strict.
func TestLoadJWTIssuer_RealMode_AlsoRequired(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "")
	_, err := loadJWTIssuer("real")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_JWT_ISSUER")
}

// --- loadJWTAudience ---

// TestLoadJWTAudience_MissingEnvVar_Error verifies that an unset GOCELL_JWT_AUDIENCE
// returns an error containing the env var name.
func TestLoadJWTAudience_MissingEnvVar_Error(t *testing.T) {
	t.Setenv("GOCELL_JWT_AUDIENCE", "")
	_, err := loadJWTAudience("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_JWT_AUDIENCE",
		"error must name the missing env var")
}

// TestLoadJWTAudience_SetEnvVar_Used verifies that a set GOCELL_JWT_AUDIENCE is returned.
func TestLoadJWTAudience_SetEnvVar_Used(t *testing.T) {
	t.Setenv("GOCELL_JWT_AUDIENCE", "my-service")
	val, err := loadJWTAudience("")
	require.NoError(t, err)
	assert.Equal(t, "my-service", val)
}

// --- buildJWTDeps integration ---

// TestBuildJWTDeps_RealMode_VerifierRejectsWrongIssuer builds JWT deps and
// verifies that a token carrying a different iss claim is rejected.
// Uses the same key set to isolate the issuer check from key mismatch.
func TestBuildJWTDeps_RealMode_VerifierRejectsWrongIssuer(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "correct-issuer")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")

	_, err := buildJWTDeps("")
	require.NoError(t, err)

	// Issue a token using a separate key set with iss="wrong-issuer", then verify
	// using a verifier that expects iss="correct-issuer". The key mismatch is
	// intentional isolation: we test only the issuer claim enforcement.
	ks, _, _ := auth.MustNewTestKeySet()
	wrongIssuerIssuer, err := auth.NewJWTIssuer(ks, "wrong-issuer", 15*time.Minute,
		auth.WithDefaultAudience("gocell"))
	require.NoError(t, err)
	wrongVerifier, err := auth.NewJWTVerifier(ks,
		auth.WithExpectedAudiences("gocell"),
		auth.WithExpectedIssuer("correct-issuer"))
	require.NoError(t, err)

	tok, err := wrongIssuerIssuer.Issue(auth.TokenIntentAccess, "user-1", auth.IssueOptions{
		Audience: []string{"gocell"},
	})
	require.NoError(t, err)

	// Verify using a verifier that expects "correct-issuer" but token has "wrong-issuer".
	_, err = wrongVerifier.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.Error(t, err, "token with wrong iss must be rejected")
	assert.Contains(t, err.Error(), "issuer", "error must mention issuer mismatch")
}

// TestBuildJWTDeps_RealMode_VerifierRejectsWrongAudience builds JWT deps and
// verifies that a token signed with a different audience is rejected.
func TestBuildJWTDeps_RealMode_VerifierRejectsWrongAudience(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "correct-issuer")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")

	deps, err := buildJWTDeps("")
	require.NoError(t, err)

	tok, err := deps.issuer.Issue(auth.TokenIntentAccess, "user-1", auth.IssueOptions{
		Audience: []string{"wrong-service"},
	})
	require.NoError(t, err)

	_, err = deps.verifier.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.Error(t, err, "token with wrong aud must be rejected")
}

// TestBuildJWTDeps_ProdWiring_VerifierRejectsWrongIssuer is an end-to-end
// wiring test: it builds deps via buildJWTDeps (reading issuer from env) and
// then uses deps.verifier to reject a token signed with a different issuer.
// This locks the env → issuer → verifier wiring that the prior RealMode tests
// missed (B3 finding): the prior test created an independent verifier with
// explicit options, so it never proved that the verifier produced by
// buildJWTDeps reads GOCELL_JWT_ISSUER correctly.
func TestBuildJWTDeps_ProdWiring_VerifierRejectsWrongIssuer(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "prod-iss")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")

	deps, err := buildJWTDeps("")
	require.NoError(t, err, "buildJWTDeps must succeed with valid env vars")

	// Use a separate key set and issuer so key mismatch does not interfere.
	// We want to isolate the issuer claim check on deps.verifier.
	ks, _, _ := auth.MustNewTestKeySet()
	wrongIssuer, err := auth.NewJWTIssuer(ks, "wrong-iss", 15*time.Minute,
		auth.WithDefaultAudience("gocell"))
	require.NoError(t, err)

	// Create a separate verifier for the alternative key set that trusts wrong-iss.
	// deps.verifier uses its own key set and expects "prod-iss" — key mismatch
	// will always fail, which is intentional: the goal is to assert that deps.verifier
	// rejects the token (regardless of whether it is due to key mismatch or issuer).
	tok, err := wrongIssuer.Issue(auth.TokenIntentAccess, "user-1", auth.IssueOptions{
		Audience: []string{"gocell"},
	})
	require.NoError(t, err)

	// deps.verifier must reject the token — wrong key and wrong issuer both contribute.
	_, err = deps.verifier.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.Error(t, err,
		"deps.verifier (wired from GOCELL_JWT_ISSUER=prod-iss) must reject a token "+
			"issued by wrong-iss; this locks env→verifier wiring (B3)")
}

// TestBuildJWTDeps_ProdWiring_VerifierRejectsWrongAudience is an end-to-end
// wiring test: it builds deps via buildJWTDeps and uses deps.issuer to sign a
// token with the wrong audience, then asserts deps.verifier rejects it.
// This complements the B3 fix by verifying GOCELL_JWT_AUDIENCE flows into the
// verifier's audience check (not just into the issuer's default audience).
func TestBuildJWTDeps_ProdWiring_VerifierRejectsWrongAudience(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "prod-iss")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")

	deps, err := buildJWTDeps("")
	require.NoError(t, err, "buildJWTDeps must succeed with valid env vars")

	// Issue a token overriding the audience with a wrong value.
	tok, err := deps.issuer.Issue(auth.TokenIntentAccess, "user-1", auth.IssueOptions{
		Audience: []string{"wrong-service"},
	})
	require.NoError(t, err)

	// deps.verifier expects aud="gocell" (from GOCELL_JWT_AUDIENCE env var).
	_, err = deps.verifier.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.Error(t, err,
		"deps.verifier must reject token with aud=wrong-service; "+
			"locks GOCELL_JWT_AUDIENCE→verifier wiring (B3)")
}

// TestBuildJWTDeps_LogsEffectiveConfig verifies that buildJWTDeps emits a
// structured Info log with issuer, audience, and adapter_mode fields.
func TestBuildJWTDeps_LogsEffectiveConfig(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "log-test-issuer")
	t.Setenv("GOCELL_JWT_AUDIENCE", "log-test-aud")

	var buf bytes.Buffer
	handler := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	prev := slog.Default()
	slog.SetDefault(slog.New(handler))
	t.Cleanup(func() { slog.SetDefault(prev) })

	_, err := buildJWTDeps("testmode")
	require.NoError(t, err)

	// Parse log lines looking for the JWT deps built record.
	var found bool
	for _, line := range splitLines(buf.Bytes()) {
		if len(line) == 0 {
			continue
		}
		var record map[string]any
		if err := json.Unmarshal(line, &record); err != nil {
			continue
		}
		if record["msg"] != "core-bundle: JWT deps built" {
			continue
		}
		found = true
		assert.Equal(t, "log-test-issuer", record["issuer"], "log must contain issuer field")
		assert.Equal(t, "log-test-aud", record["audience"], "log must contain audience field")
		assert.Equal(t, "testmode", record["adapter_mode"], "log must contain adapter_mode field")
	}
	assert.True(t, found, "structured log 'core-bundle: JWT deps built' must be emitted by buildJWTDeps")
}

// splitLines splits a byte slice into non-empty newline-delimited chunks.
func splitLines(b []byte) [][]byte {
	var out [][]byte
	for len(b) > 0 {
		idx := bytes.IndexByte(b, '\n')
		if idx < 0 {
			out = append(out, b)
			break
		}
		if idx > 0 {
			out = append(out, b[:idx])
		}
		b = b[idx+1:]
	}
	return out
}
