// Tests for env-var-driven JWT dependency loading (C5 / F1 Registry).
//
// GOCELL_JWT_ISSUER and GOCELL_JWT_AUDIENCE are required in all adapter modes
// (there is no fallback default value). After F1, loading is done via
// authconfig.FromEnv; these tests exercise buildJWTDeps end-to-end.
package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// setTestJWTKeyEnv installs a PEM-encoded RSA key pair into GOCELL_JWT_PRIVATE_KEY /
// GOCELL_JWT_PUBLIC_KEY so that a subsequent loadKeySet("") call reads the same
// key material the caller can reload via auth.LoadKeySetFromEnv. This eliminates
// the hidden key-mismatch dimension that would otherwise mask wiring regressions
// in issuer/audience validation tests (B3 wiring lock integrity).
//
// Returns nothing: state is restored by t.Setenv at the end of the test.
func setTestJWTKeyEnv(t *testing.T) {
	t.Helper()
	priv, pub := auth.MustGenerateTestKeyPair()
	privPEM := pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(priv),
	})
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err, "marshal test public key")
	pubPEM := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER})
	t.Setenv(auth.EnvJWTPrivateKey, string(privPEM))
	t.Setenv(auth.EnvJWTPublicKey, string(pubPEM))
}

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

// --- buildJWTDeps wiring integration ---
//
// These tests lock env → Registry → issuer/verifier wiring (B3) by isolating
// each failure dimension. The key material is injected via env so deps and the
// test-side issuer share the SAME keyset — a signature failure would otherwise
// short-circuit before iss/aud claim checks, turning the test into a false
// positive that passes even when issuer/audience wiring is broken.

// TestBuildJWTDeps_ProdWiring_VerifierRejectsWrongIssuer locks the
// GOCELL_JWT_ISSUER → Registry → verifier.expectedIssuer wiring. The token is
// signed with deps' own keyset (via env) but carries iss="wrong-iss"; signature
// verification passes, then the issuer check rejects. We assert the specific
// errcode (ErrAuthInvalidTokenIntent) and message ("issuer") so a regression
// into a signature-level failure — which would also produce a non-nil error —
// is caught.
func TestBuildJWTDeps_ProdWiring_VerifierRejectsWrongIssuer(t *testing.T) {
	setTestJWTKeyEnv(t)
	t.Setenv("GOCELL_JWT_ISSUER", "prod-iss")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")

	deps, err := buildJWTDeps("")
	require.NoError(t, err, "buildJWTDeps must succeed with valid env vars")

	// Reload the exact same key material from env — deps already loaded it, so
	// tokens signed with this ks are signature-valid against deps.verifier.
	ks, err := auth.LoadKeySetFromEnv()
	require.NoError(t, err, "reload keyset from env to mirror deps wiring")

	wrongIssuer, err := auth.NewJWTIssuer(ks, "wrong-iss", 15*time.Minute,
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)

	tok, err := wrongIssuer.Issue(auth.TokenIntentAccess, "user-1", auth.IssueOptions{})
	require.NoError(t, err)

	_, err = deps.verifier.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.Error(t, err,
		"deps.verifier (wired from GOCELL_JWT_ISSUER=prod-iss) must reject token with iss=wrong-iss")

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr),
		"rejection must be an *errcode.Error (got %T); otherwise upstream wiring may be bypassing Registry", err)
	assert.Equal(t, errcode.ErrAuthInvalidTokenIntent, ecErr.Code,
		"wrong-iss must surface ErrAuthInvalidTokenIntent; a different code means either "+
			"signature verification failed first (key-mismatch pseudo-positive) or the iss check did not run")
	// The safe client-facing Message distinguishes iss vs aud branches even
	// though both share a single error code (enumeration-defense collapses
	// them at the HTTP layer; see runtime/auth/jwt.go::checkIssuer).
	assert.Contains(t, strings.ToLower(ecErr.Message), "issuer",
		"Message must mention 'issuer' so a regression swapping the iss/aud branches is caught")
}

// TestBuildJWTDeps_ProdWiring_VerifierRejectsWrongAudience locks the
// GOCELL_JWT_AUDIENCE → Registry → verifier.expectedAudiences wiring.
// deps.issuer is used to sign (same key) but with an overridden wrong audience.
func TestBuildJWTDeps_ProdWiring_VerifierRejectsWrongAudience(t *testing.T) {
	setTestJWTKeyEnv(t)
	t.Setenv("GOCELL_JWT_ISSUER", "prod-iss")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")

	deps, err := buildJWTDeps("")
	require.NoError(t, err, "buildJWTDeps must succeed with valid env vars")

	tok, err := deps.issuer.Issue(auth.TokenIntentAccess, "user-1", auth.IssueOptions{
		Audience: []string{"wrong-service"},
	})
	require.NoError(t, err)

	_, err = deps.verifier.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.Error(t, err, "deps.verifier must reject token with aud=wrong-service")

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr), "rejection must be an *errcode.Error, got %T", err)
	assert.Equal(t, errcode.ErrAuthInvalidTokenIntent, ecErr.Code,
		"wrong-aud must surface ErrAuthInvalidTokenIntent")
	assert.Contains(t, strings.ToLower(ecErr.Message), "audience",
		"Message must mention 'audience' so a regression swapping iss/aud branches is caught")
}

// TestBuildJWTDeps_ProdWiring_VerifierRejectsWrongKey isolates the signature
// verification dimension: a token signed by a key NOT known to deps.verifier
// must be rejected with ErrAuthUnauthorized (distinct code from iss/aud
// mismatches). This complements the WrongIssuer/WrongAudience tests, ensuring
// all three failure modes remain independently distinguishable.
func TestBuildJWTDeps_ProdWiring_VerifierRejectsWrongKey(t *testing.T) {
	setTestJWTKeyEnv(t)
	t.Setenv("GOCELL_JWT_ISSUER", "prod-iss")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")

	deps, err := buildJWTDeps("")
	require.NoError(t, err)

	// Build a completely independent keyset — deps.verifier has no public key
	// matching this kid, so verification must fail at the signature step.
	strangerKS, _, _ := auth.MustNewTestKeySet()
	stranger, err := auth.NewJWTIssuer(strangerKS, "prod-iss", 15*time.Minute,
		auth.WithIssuerAudiencesFromSlice([]string{"gocell"}))
	require.NoError(t, err)

	tok, err := stranger.Issue(auth.TokenIntentAccess, "user-1", auth.IssueOptions{})
	require.NoError(t, err)

	_, err = deps.verifier.VerifyIntent(context.Background(), tok, auth.TokenIntentAccess)
	require.Error(t, err, "deps.verifier must reject token signed by unknown key")

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr), "rejection must be an *errcode.Error, got %T", err)
	assert.Equal(t, errcode.ErrAuthUnauthorized, ecErr.Code,
		"unknown signing key must surface ErrAuthUnauthorized (signature failure), not ErrAuthInvalidTokenIntent")
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
