// Tests for env-var-driven JWT dependency loading (C5 / F1 Registry).
//
// GOCELL_JWT_ISSUER and GOCELL_JWT_AUDIENCE are required in all adapter modes
// (there is no fallback default value). After F1, loading is done via
// authconfig.FromEnv; these tests exercise buildJWTDeps end-to-end.
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

// --- buildJWTDeps: env-var validation ---

// TestBuildJWTDeps_MissingIssuer_Error verifies that an unset GOCELL_JWT_ISSUER
// causes buildJWTDeps to fail fast.
func TestBuildJWTDeps_MissingIssuer_Error(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")
	_, err := buildJWTDeps("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_JWT_ISSUER",
		"error must name the missing env var")
}

// TestBuildJWTDeps_MissingAudience_Error verifies that an unset GOCELL_JWT_AUDIENCE
// causes buildJWTDeps to fail fast.
func TestBuildJWTDeps_MissingAudience_Error(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "gocell-prod")
	t.Setenv("GOCELL_JWT_AUDIENCE", "")
	_, err := buildJWTDeps("")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_JWT_AUDIENCE",
		"error must name the missing env var")
}

// --- buildJWTDeps integration ---

// TestBuildJWTDeps_RealMode_VerifierRejectsWrongIssuer builds JWT deps and
// verifies that a token carrying a different iss claim is rejected.
func TestBuildJWTDeps_RealMode_VerifierRejectsWrongIssuer(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "correct-issuer")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")

	_, err := buildJWTDeps("")
	require.NoError(t, err)

	// Issue a token using a separate key set with iss="wrong-issuer", then verify
	// using a verifier that expects iss="correct-issuer".
	ks, _, _ := auth.MustNewTestKeySet()
	wrongIssuerIssuer, err := auth.NewJWTIssuer(ks, "wrong-issuer", 15*time.Minute,
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
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
// Locks env → Registry → issuer → verifier wiring (B3).
func TestBuildJWTDeps_ProdWiring_VerifierRejectsWrongIssuer(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "prod-iss")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")

	deps, err := buildJWTDeps("")
	require.NoError(t, err, "buildJWTDeps must succeed with valid env vars")

	// Use a separate key set and issuer so key mismatch does not interfere.
	ks, _, _ := auth.MustNewTestKeySet()
	wrongIssuer, err := auth.NewJWTIssuer(ks, "wrong-iss", 15*time.Minute,
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)

	tok, err := wrongIssuer.Issue(auth.TokenIntentAccess, "user-1", auth.IssueOptions{
		Audience: []string{"gocell"},
	})
	require.NoError(t, err)

	// deps.verifier must reject the token — wrong key and wrong issuer both contribute.
	_, err = deps.verifier.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.Error(t, err,
		"deps.verifier (wired from GOCELL_JWT_ISSUER=prod-iss) must reject a token "+
			"issued by wrong-iss; locks env→Registry→verifier wiring (B3)")
}

// TestBuildJWTDeps_ProdWiring_VerifierRejectsWrongAudience is an end-to-end
// wiring test: builds deps and verifies GOCELL_JWT_AUDIENCE flows into verifier.
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
			"locks GOCELL_JWT_AUDIENCE→Registry→verifier wiring (B3)")
}

// TestBuildJWTDeps_LogsEffectiveConfig verifies that buildJWTDeps emits a
// structured Info log with issuer, audiences, and adapter_mode fields.
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
		assert.Equal(t, "testmode", record["adapter_mode"], "log must contain adapter_mode field")
		// audiences is now a []string (slog.Any) rather than a plain string
		assert.NotNil(t, record["audiences"], "log must contain audiences field")
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
