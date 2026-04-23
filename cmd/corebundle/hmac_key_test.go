package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildHMACKey_DevDefault_Succeeds confirms that in dev mode (empty
// AdapterMode) with an empty Primary, the DevDefault is returned as bytes.
func TestBuildHMACKey_DevDefault_Succeeds(t *testing.T) {
	key, err := buildHMACKey(hmacKeyConfig{
		AdapterMode: "",
		EnvLabel:    "GOCELL_AUDITCORE_HMAC_KEY",
		Primary:     "",
		DevDefault:  "dev-hmac-key-replace-in-prod!!!!",
		Label:       "auditcore HMAC",
	})
	require.NoError(t, err)
	assert.Equal(t, []byte("dev-hmac-key-replace-in-prod!!!!"), key)
}

// TestBuildHMACKey_RealModePrimaryEmpty_FailFast asserts that in adapter
// mode "real" an empty Primary triggers the hard-error branch, and the error
// contains the label, env label, and adapter mode for operator diagnosis.
func TestBuildHMACKey_RealModePrimaryEmpty_FailFast(t *testing.T) {
	key, err := buildHMACKey(hmacKeyConfig{
		AdapterMode: "real",
		EnvLabel:    "GOCELL_AUDITCORE_HMAC_KEY",
		Primary:     "",
		DevDefault:  "dev-hmac-key-replace-in-prod!!!!",
		Label:       "auditcore HMAC",
	})
	require.Error(t, err)
	assert.Nil(t, key)
	assert.Contains(t, err.Error(), "auditcore HMAC", "error must include label")
	assert.Contains(t, err.Error(), "GOCELL_AUDITCORE_HMAC_KEY", "error must name the env var")
	assert.Contains(t, err.Error(), "adapter mode \"real\"", "error must indicate the triggering mode")
}

// TestBuildHMACKey_DemoKeyInRealMode_Rejected asserts that a well-known demo
// value is rejected in real mode even when Primary is set.
func TestBuildHMACKey_DemoKeyInRealMode_Rejected(t *testing.T) {
	key, err := buildHMACKey(hmacKeyConfig{
		AdapterMode: "real",
		EnvLabel:    "GOCELL_AUDITCORE_HMAC_KEY",
		Primary:     "dev-hmac-key-replace-in-prod!!!!",
		DevDefault:  "dev-hmac-key-replace-in-prod!!!!",
		Label:       "auditcore HMAC",
	})
	require.Error(t, err)
	assert.Nil(t, key)
	assert.Contains(t, err.Error(), "well-known demo key",
		"error must come from rejectDemoKey")
	assert.Contains(t, err.Error(), "GOCELL_AUDITCORE_HMAC_KEY")
}

// TestBuildHMACKey_HappyPath asserts that a non-demo primary key in real mode
// is returned as-is without error.
func TestBuildHMACKey_HappyPath(t *testing.T) {
	fresh := "this-is-a-fresh-real-hmac-key-!!"
	key, err := buildHMACKey(hmacKeyConfig{
		AdapterMode: "real",
		EnvLabel:    "GOCELL_AUDITCORE_HMAC_KEY",
		Primary:     fresh,
		DevDefault:  "dev-hmac-key-replace-in-prod!!!!",
		Label:       "auditcore HMAC",
	})
	require.NoError(t, err)
	assert.Equal(t, []byte(fresh), key)
}

// TestBuildHMACKey_DevMode_NonDemoPrimary_Succeeds asserts that in dev mode
// with a non-demo primary value, the primary is returned directly.
func TestBuildHMACKey_DevMode_NonDemoPrimary_Succeeds(t *testing.T) {
	primary := "custom-dev-hmac-key-value-here!!"
	key, err := buildHMACKey(hmacKeyConfig{
		AdapterMode: "",
		EnvLabel:    "GOCELL_AUDITCORE_HMAC_KEY",
		Primary:     primary,
		DevDefault:  "dev-hmac-key-replace-in-prod!!!!",
		Label:       "auditcore HMAC",
	})
	require.NoError(t, err)
	assert.Equal(t, []byte(primary), key)
}
