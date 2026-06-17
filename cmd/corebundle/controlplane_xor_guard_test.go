// controlplane_xor_guard_test.go — XOR env-layer guard for buildInternalServiceKeyring.
//
// Verifies the mutual-exclusion (#2153) invariant: master mode (GOCELL_SERVICE_SECRET)
// and provisioned mode (GOCELL_SERVICE_SIGNING_KEY + GOCELL_SERVICE_VERIFY_KEYS)
// are mutually exclusive. Setting both is ambiguous (a split cell must never hold the
// master); setting neither leaves the control plane unprotected — both must fail-fast.
package main

import (
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// TestBuildInternalServiceKeyring_XOR_BothSet verifies that setting both
// GOCELL_SERVICE_SECRET (master mode) and GOCELL_SERVICE_SIGNING_KEY
// (provisioned mode) causes buildInternalServiceKeyring to return an error.
// A split cell must hold only its per-cell subkeys and must never also hold the
// master secret — the two modes are architecturally mutually exclusive (#2153).
func TestBuildInternalServiceKeyring_XOR_BothSet(t *testing.T) {
	t.Setenv(auth.EnvServiceSecret, freshTestServiceSecret(t))
	// Provide a syntactically valid (though not semantically meaningful) signing key.
	t.Setenv(auth.EnvServiceSigningKey, hex32hex(t))
	// Ensure no leftover provisioned verify-key env interferes.
	t.Setenv(auth.EnvServiceVerifyKeys, "")

	_, err := buildInternalServiceKeyring("")
	require.Error(t, err, "both master and provisioned env set must return error")
	assert.Contains(t, err.Error(), auth.EnvServiceSecret,
		"error must mention master env var so operator can diagnose the conflict")
	assert.Contains(t, err.Error(), auth.EnvServiceSigningKey,
		"error must mention provisioned env var so operator can diagnose the conflict")
}

// TestBuildInternalServiceKeyring_XOR_NeitherSet verifies that when neither
// GOCELL_SERVICE_SECRET nor GOCELL_SERVICE_SIGNING_KEY is set,
// buildInternalServiceKeyring returns an error. The control plane must not start
// without one of the two configured key modes.
func TestBuildInternalServiceKeyring_XOR_NeitherSet(t *testing.T) {
	t.Setenv(auth.EnvServiceSecret, "")
	t.Setenv(auth.EnvServiceSigningKey, "")

	_, err := buildInternalServiceKeyring("")
	require.Error(t, err, "neither master nor provisioned env set must return error")
	// The error must name at least one of the two required env vars.
	hasSecret := assert.Contains(t, err.Error(), auth.EnvServiceSecret)
	hasSigningKey := assert.Contains(t, err.Error(), auth.EnvServiceSigningKey)
	assert.True(t, hasSecret || hasSigningKey,
		"error must name at least one required env var")
}

// TestBuildInternalServiceKeyring_XOR_OnlyProvisioned verifies that setting only
// GOCELL_SERVICE_CELL + GOCELL_SERVICE_SIGNING_KEY + GOCELL_SERVICE_VERIFY_KEYS
// (without GOCELL_SERVICE_SECRET) produces a non-nil, usable ServiceKeyring.
//
// Key derivation uses DeriveProvisionedKeys (the same function the CLI uses),
// so the env values injected here are byte-identical to what the operator would
// provision for a real split deployment.
func TestBuildInternalServiceKeyring_XOR_OnlyProvisioned(t *testing.T) {
	// Build a master ring for key derivation only — it is NOT set in env.
	master, err := auth.NewHMACKeyRing([]byte("test-master-secret-32-bytes-long!"), nil)
	require.NoError(t, err)

	const ownCell = "configcore"
	callers := []string{"accesscore"}

	// Derive provisioned keys (same as `gocell derive-service-keys`).
	pk, err := auth.DeriveProvisionedKeys(master, ownCell, callers)
	require.NoError(t, err)

	// Encode keys as env vars.
	signingHex := hex.EncodeToString(pk.SigningCurrent)
	verifyEnv := auth.FormatVerifyKeys(pk.VerifyCurrent)

	// Inject ONLY provisioned env vars — master env must be absent.
	t.Setenv(auth.EnvServiceSecret, "")
	t.Setenv(auth.EnvServiceSigningKeyPrevious, "")
	t.Setenv(auth.EnvServiceOwnCell, ownCell)
	t.Setenv(auth.EnvServiceSigningKey, signingHex)
	t.Setenv(auth.EnvServiceVerifyKeys, verifyEnv)
	t.Setenv(auth.EnvServiceVerifyKeysPrevious, "")

	ring, err := buildInternalServiceKeyring("")
	require.NoError(t, err, "provisioned-only mode must succeed")
	require.NotNil(t, ring, "provisioned keyring must be non-nil")

	// Validate the ring is self-consistent (Validate passes).
	require.NoError(t, ring.Validate(), "returned keyring must pass Validate()")

	// Signing as ownCell must succeed; signing as a different cell must fail.
	signingKeys, err := ring.SigningSecrets(ownCell)
	require.NoError(t, err, "SigningSecrets(ownCell) must succeed")
	assert.NotEmpty(t, signingKeys, "signing keys must be non-empty")

	_, err = ring.SigningSecrets("auditcore")
	assert.Error(t, err, "signing as a non-own cell must fail (split isolation)")
}

// TestBuildInternalServiceKeyring_XOR_OnlyProvisioned_ByteConsistency asserts
// that the wiring layer's buildInternalServiceKeyring produces a keyring whose
// SigningSecrets(ownCell)[0] is byte-identical to the SigningCurrent produced by
// DeriveProvisionedKeys for the same master and ownCell. This pins the
// env-encode → env-decode round-trip: any drift (e.g. a re-encoding bug) would
// change the key bytes and silently break cross-cell token verification.
func TestBuildInternalServiceKeyring_XOR_OnlyProvisioned_ByteConsistency(t *testing.T) {
	master, err := auth.NewHMACKeyRing([]byte("test-master-secret-32-bytes-long!"), nil)
	require.NoError(t, err)

	const ownCell = "configcore"
	callers := []string{"accesscore"}

	// Derive the canonical key material using DeriveProvisionedKeys.
	pk, err := auth.DeriveProvisionedKeys(master, ownCell, callers)
	require.NoError(t, err)

	// Encode and inject into env — same path the CLI / operator would use.
	signingHex := hex.EncodeToString(pk.SigningCurrent)
	verifyEnv := auth.FormatVerifyKeys(pk.VerifyCurrent)

	t.Setenv(auth.EnvServiceSecret, "")
	t.Setenv(auth.EnvServiceSigningKeyPrevious, "")
	t.Setenv(auth.EnvServiceOwnCell, ownCell)
	t.Setenv(auth.EnvServiceSigningKey, signingHex)
	t.Setenv(auth.EnvServiceVerifyKeys, verifyEnv)
	t.Setenv(auth.EnvServiceVerifyKeysPrevious, "")

	ring, err := buildInternalServiceKeyring("")
	require.NoError(t, err)
	require.NotNil(t, ring)

	// The wiring layer must produce signing keys that exactly match
	// DeriveProvisionedKeys(master, ownCell).SigningCurrent.
	signingFromRing, err := ring.SigningSecrets(ownCell)
	require.NoError(t, err)
	require.NotEmpty(t, signingFromRing, "SigningSecrets must return at least one key")

	assert.Equal(t, pk.SigningCurrent, signingFromRing[0],
		"ring.SigningSecrets(ownCell)[0] must be byte-identical to DeriveProvisionedKeys.SigningCurrent; "+
			"any drift means env encode/decode is broken and cross-cell token verification will fail silently")
}

// TestBuildInternalServiceKeyring_XOR_MasterPlusPartialSplitEnv verifies that the
// XOR guard detects the ambiguous both-modes case even when the split config is
// PARTIAL — e.g. the master secret plus only GOCELL_SERVICE_CELL (no signing key).
// Deriving provisioned mode from the signing-key sentinel alone (the #2153 F1 bug)
// let such a config slip past the guard and silently boot master mode while still
// holding the master — exactly what a split cell must never do. provisionedMode
// must derive from the FULL split env family, so the master secret plus ANY split
// env var fails closed.
func TestBuildInternalServiceKeyring_XOR_MasterPlusPartialSplitEnv(t *testing.T) {
	// Each split env var, on its own, must be enough to make "master also set"
	// ambiguous. The signing key is covered by _XOR_BothSet; this pins the rest.
	partialSplitVars := []string{
		auth.EnvServiceOwnCell,
		auth.EnvServiceSigningKeyPrevious,
		auth.EnvServiceVerifyKeys,
		auth.EnvServiceVerifyKeysPrevious,
	}
	for _, v := range partialSplitVars {
		t.Run(v, func(t *testing.T) {
			// Master present; signing-key sentinel explicitly absent; only ONE
			// partial split var set. The guard fires before any key parsing, so the
			// value shape is irrelevant — only presence matters.
			t.Setenv(auth.EnvServiceSecret, freshTestServiceSecret(t))
			t.Setenv(auth.EnvServiceOwnCell, "")
			t.Setenv(auth.EnvServiceSigningKey, "")
			t.Setenv(auth.EnvServiceSigningKeyPrevious, "")
			t.Setenv(auth.EnvServiceVerifyKeys, "")
			t.Setenv(auth.EnvServiceVerifyKeysPrevious, "")
			t.Setenv(v, "configcore")

			_, err := buildInternalServiceKeyring("")
			require.Error(t, err,
				"master + partial split env (%s) must be rejected as ambiguous, not silently boot master mode", v)
			assert.Contains(t, err.Error(), auth.EnvServiceSecret,
				"conflict error must name the master env var so the operator can diagnose it")
		})
	}
}

// hex32hex returns a 32-byte zero key hex-encoded, suitable as a syntactically
// valid (though semantically trivial) provisioned subkey in XOR guard tests that
// are only checking error paths (key length validation is not the test target).
func hex32hex(t *testing.T) string {
	t.Helper()
	return hex.EncodeToString(make([]byte, 32))
}
