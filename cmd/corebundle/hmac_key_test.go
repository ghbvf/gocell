package main

import (
	"strings"
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

// TestBuildHMACKey_DevMode_DemoKeyPrimary_Succeeds locks in the design decision
// that rejectDemoKey is a no-op in dev mode: an operator who hasn't rotated
// away from the well-known dev default must still be able to start the service
// in dev/demo topology. Real mode is the enforcement gate.
//
// Refs: PR#232 review finding P2 (dev mode must not reject demo keys).
func TestBuildHMACKey_DevMode_DemoKeyPrimary_Succeeds(t *testing.T) {
	demoKey := "dev-hmac-key-replace-in-prod!!!!"
	key, err := buildHMACKey(hmacKeyConfig{
		AdapterMode: "",
		EnvLabel:    "GOCELL_AUDITCORE_HMAC_KEY",
		Primary:     demoKey,
		DevDefault:  demoKey,
		Label:       "auditcore HMAC",
	})
	require.NoError(t, err)
	assert.Equal(t, []byte(demoKey), key,
		"dev mode must return the demo key without error — rejectDemoKey is a no-op outside real mode")
}

// TestBuildHMACKey_RealMode_ErrorFormat_NoDoubleLabel pins the expected error
// format from buildHMACKey directly: the returned error must NOT self-embed the
// label ("auditcore HMAC") — that context belongs to the module wrapper
// (audit_module.go). Embedding it here causes a double-label when the module
// prepends "auditcore HMAC key: ...".
//
// Expected post-fix: err.Error() == `GOCELL_AUDITCORE_HMAC_KEY must be set in
// adapter mode "real"` (zero occurrences of "auditcore HMAC").
//
// This test will FAIL on HEAD where buildHMACKey emits
// `"auditcore HMAC: GOCELL_AUDITCORE_HMAC_KEY must be set ..."`.
//
// Refs: PR#232 review finding P2 (double label in error chain).
func TestBuildHMACKey_RealMode_ErrorFormat_NoDoubleLabel(t *testing.T) {
	_, err := buildHMACKey(hmacKeyConfig{
		AdapterMode: "real",
		EnvLabel:    "GOCELL_AUDITCORE_HMAC_KEY",
		Primary:     "",
		DevDefault:  "dev-hmac-key-replace-in-prod!!!!",
		Label:       "auditcore HMAC",
	})
	require.Error(t, err)
	// buildHMACKey itself must NOT embed the label — the module wrapper owns that.
	assert.Equal(t, 0, strings.Count(err.Error(), "auditcore HMAC"),
		"buildHMACKey must not self-embed the label; got: %q", err.Error())
	assert.Contains(t, err.Error(), "GOCELL_AUDITCORE_HMAC_KEY",
		"error must still name the env var for operator diagnosis")
	assert.Contains(t, err.Error(), "adapter mode \"real\"",
		"error must indicate the triggering mode")
}
