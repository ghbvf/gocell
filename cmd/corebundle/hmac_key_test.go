package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/platform/platformshared"
)

// TestBuildHMACKey_DevDefault_Succeeds confirms that in dev mode (empty
// AdapterMode) with an empty Primary, the DevDefault is returned as bytes.
func TestBuildHMACKey_DevDefault_Succeeds(t *testing.T) {
	key, err := platformshared.BuildHMACKey(platformshared.HMACKeyConfig{
		AdapterMode: "",
		EnvName:     "GOCELL_AUDITCORE_HMAC_KEY",
		Primary:     "",
		DevDefault:  "dev-hmac-key-replace-in-prod!!!!",
	})
	require.NoError(t, err)
	assert.Equal(t, []byte("dev-hmac-key-replace-in-prod!!!!"), key)
}

// TestBuildHMACKey_RealModePrimaryEmpty_FailFast asserts that in adapter
// mode "real" an empty Primary triggers the hard-error branch.
func TestBuildHMACKey_RealModePrimaryEmpty_FailFast(t *testing.T) {
	key, err := platformshared.BuildHMACKey(platformshared.HMACKeyConfig{
		AdapterMode: "real",
		EnvName:     "GOCELL_AUDITCORE_HMAC_KEY",
		Primary:     "",
		DevDefault:  "dev-hmac-key-replace-in-prod!!!!",
	})
	require.Error(t, err)
	assert.Nil(t, key)
	assert.Contains(t, err.Error(), "GOCELL_AUDITCORE_HMAC_KEY",
		"error must contain the env var name for operator diagnosis")
	assert.Contains(t, err.Error(), "real",
		"error must mention the adapter mode that triggered the fail-fast")
}

// TestBuildHMACKey_RealModeRejectsDemoKey verifies that a well-known demo key
// is rejected in real mode.
func TestBuildHMACKey_RealModeRejectsDemoKey(t *testing.T) {
	key, err := platformshared.BuildHMACKey(platformshared.HMACKeyConfig{
		AdapterMode: "real",
		EnvName:     "GOCELL_AUDITCORE_HMAC_KEY",
		Primary:     "dev-hmac-key-replace-in-prod!!!!",
		DevDefault:  "dev-hmac-key-replace-in-prod!!!!",
	})
	require.Error(t, err)
	assert.Nil(t, key)
	assert.Contains(t, err.Error(), "well-known demo key")
}

// TestBuildHMACKey_DevModeAcceptsDemoKey verifies that a well-known demo key is
// accepted in dev mode (not real mode).
func TestBuildHMACKey_DevModeAcceptsDemoKey(t *testing.T) {
	key, err := platformshared.BuildHMACKey(platformshared.HMACKeyConfig{
		AdapterMode: "",
		EnvName:     "GOCELL_AUDITCORE_HMAC_KEY",
		Primary:     "dev-hmac-key-replace-in-prod!!!!",
		DevDefault:  "dev-hmac-key-replace-in-prod!!!!",
	})
	require.NoError(t, err)
	assert.NotNil(t, key)
}

// TestBuildHMACKey_RealModeWithFreshKey verifies that real mode accepts a
// production-quality HMAC key.
func TestBuildHMACKey_RealModeWithFreshKey(t *testing.T) {
	freshKey := strings.Repeat("x", 32)
	key, err := platformshared.BuildHMACKey(platformshared.HMACKeyConfig{
		AdapterMode: "real",
		EnvName:     "GOCELL_AUDITCORE_HMAC_KEY",
		Primary:     freshKey,
		DevDefault:  "dev-hmac-key-replace-in-prod!!!!",
	})
	require.NoError(t, err)
	assert.Equal(t, []byte(freshKey), key)
}
