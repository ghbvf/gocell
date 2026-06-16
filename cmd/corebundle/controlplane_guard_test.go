// Tests for controlplane service-token guard wiring (C6).
//
// buildInternalHMACRing returns a non-nil *auth.HMACKeyRing when
// GOCELL_SERVICE_SECRET is set. Missing secret is a hard error in every mode.
package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// TestBuildInternalHMACRing_DevMode_MissingSecret_ReturnsError verifies that
// dev mode (empty adapterMode) now requires GOCELL_SERVICE_SECRET — the
// previous behavior of returning (nil, nil) to silently disable the guard in
// non-real modes has been removed by the SEC-FAIL-CLOSED change.
func TestBuildInternalHMACRing_DevMode_MissingSecret_ReturnsError(t *testing.T) {
	t.Setenv("GOCELL_SERVICE_SECRET", "")
	_, err := buildInternalHMACRing("") // dev mode
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_SERVICE_SECRET",
		"all modes must fail fast when service secret is unset")
}

func TestBuildInternalHMACRing_RealMode_MissingSecret_Error(t *testing.T) {
	t.Setenv("GOCELL_SERVICE_SECRET", "")
	_, err := buildInternalHMACRing("real")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_SERVICE_SECRET",
		"real mode must fail fast when service secret is unset")
}

func TestBuildInternalHMACRing_WithSecret_ReturnsRing(t *testing.T) {
	t.Setenv("GOCELL_SERVICE_SECRET", freshTestServiceSecret(t))
	ring, err := buildInternalHMACRing("")
	require.NoError(t, err)
	assert.NotNil(t, ring, "non-empty secret must produce a non-nil ring")
}

// TestBuildInternalHMACRing_RealMode_DemoServiceSecret_Rejected verifies that
// buildInternalHMACRing returns an error when GOCELL_SERVICE_SECRET is set to
// the well-known demo value in real adapter mode. Guards against an attacker
// forging ServiceTokens using the public demo secret shipped in test fixtures.
func TestBuildInternalHMACRing_RealMode_DemoServiceSecret_Rejected(t *testing.T) {
	t.Setenv("GOCELL_SERVICE_SECRET", "service-secret-32-bytes-xxxxxx!!")
	_, err := buildInternalHMACRing("real")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_SERVICE_SECRET",
		"error must name the offending env var")
	assert.Contains(t, err.Error(), "well-known demo key",
		"error must indicate the reason")
}

// TestBuildInternalHMACRing_DevMode_DemoServiceSecret_Allowed verifies that
// the demo key check is a no-op outside of real adapter mode, preserving
// the dev/test workflow where demo fixture values are acceptable.
func TestBuildInternalHMACRing_DevMode_DemoServiceSecret_Allowed(t *testing.T) {
	t.Setenv("GOCELL_SERVICE_SECRET", "service-secret-32-bytes-xxxxxx!!")
	ring, err := buildInternalHMACRing("") // dev mode
	require.NoError(t, err)
	assert.NotNil(t, ring, "dev mode must accept demo key and return a ring")
}

// TestBuildInternalHMACRing_RealMode_DemoPreviousServiceSecret_Rejected verifies
// that GOCELL_SERVICE_SECRET_PREVIOUS is also checked against the demo blocklist
// in real adapter mode.
func TestBuildInternalHMACRing_RealMode_DemoPreviousServiceSecret_Rejected(t *testing.T) {
	t.Setenv("GOCELL_SERVICE_SECRET", freshTestServiceSecret(t))
	t.Setenv("GOCELL_SERVICE_SECRET_PREVIOUS", "service-secret-32-bytes-xxxxxx!!")
	_, err := buildInternalHMACRing("real")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_SERVICE_SECRET_PREVIOUS",
		"error must name the offending env var")
	assert.Contains(t, err.Error(), "well-known demo key",
		"error must indicate the reason")
}

// TestBuildInternalHMACRing_RealMode_RingInstalledWithSecret pins the S32
// invariant: in real adapter mode, presence of GOCELL_SERVICE_SECRET MUST
// produce a non-nil ring. service-token is currently the sole transport
// authenticator for /internal/v1/* (no mTLS yet), so a nil ring here
// silently exposes the control plane.
func TestBuildInternalHMACRing_RealMode_RingInstalledWithSecret(t *testing.T) {
	t.Setenv("GOCELL_SERVICE_SECRET", freshTestServiceSecret(t))
	ring, err := buildInternalHMACRing("real")
	require.NoError(t, err)
	require.NotNil(t, ring,
		"real mode with valid service secret must install a non-nil ring")
}

// TestBuildInternalHMACRing_RequiresSecretInAllModes verifies that
// buildInternalHMACRing returns an error when GOCELL_SERVICE_SECRET is empty,
// regardless of the adapterMode parameter.
func TestBuildInternalHMACRing_RequiresSecretInAllModes(t *testing.T) {
	// No t.Parallel() here: subtests call t.Setenv which requires sequential execution.

	modes := []string{"", "memory", "postgres", "real"}

	for _, mode := range modes {
		t.Run("mode="+mode, func(t *testing.T) {
			// No t.Parallel(): t.Setenv must run sequentially (Go testing constraint).
			t.Setenv("GOCELL_SERVICE_SECRET", "")

			ring, err := buildInternalHMACRing(mode)

			if err == nil {
				t.Errorf("buildInternalHMACRing(mode=%q): expected error for empty GOCELL_SERVICE_SECRET, got nil (ring=%v)",
					mode, ring)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "GOCELL_SERVICE_SECRET",
				"error must name GOCELL_SERVICE_SECRET env var")
		})
	}
}

// TestBuildInternalHMACRing_ValidSecret_RingUsable verifies the ring returned
// by buildInternalHMACRing can be used to build an HMACKeyRing (non-nil, no error).
func TestBuildInternalHMACRing_ValidSecret_RingUsable(t *testing.T) {
	secret := freshTestServiceSecret(t)
	t.Setenv("GOCELL_SERVICE_SECRET", secret)

	ring, err := buildInternalHMACRing("")
	require.NoError(t, err)
	require.NotNil(t, ring, "ring must be installed when secret is present")

	// Verify ring works by generating a service token (non-empty result = usable ring).
	token := auth.GenerateServiceToken(ring, "accesscore", "GET", "/internal/v1/access/roles", "", "", "", time.Now())
	assert.NotEmpty(t, token, "ring must produce valid service tokens")
}
