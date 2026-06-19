package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
)

// TestOperatorAdminOptions verifies the operator control-plane wiring at the FINAL
// assembly layer (#1755 F4) — the gap the helper-only test could not close. The
// AdminListener and the audit chain verify endpoint are a COUPLED pair: both are
// wired when operator credentials are present, or neither. The len==2 assertion is
// the regression guard — dropping WithAuditChainVerifyEndpoint() (a dead AdminListener
// with no endpoint to serve) makes len==1 and trips this test.
func TestOperatorAdminOptions(t *testing.T) {
	t.Run("present credentials wire AdminListener + verify endpoint", func(t *testing.T) {
		t.Setenv(operatorAdminUsernameEnv, "ops")
		t.Setenv(operatorAdminPasswordEnv, "s3cret-operator-pw")
		t.Setenv(adminHTTPAddrEnv, "127.0.0.1:19092")
		opts, err := operatorAdminOptions(clock.Real())
		require.NoError(t, err)
		assert.Len(t, opts, 2,
			"present operator credentials must wire BOTH the AdminListener and WithAuditChainVerifyEndpoint")
	})

	t.Run("absent credentials wire nothing", func(t *testing.T) {
		t.Setenv(operatorAdminUsernameEnv, "")
		t.Setenv(operatorAdminPasswordEnv, "")
		opts, err := operatorAdminOptions(clock.Real())
		require.NoError(t, err)
		assert.Nil(t, opts, "no operator credentials must leave the admin plane (and verify endpoint) unwired")
	})

	t.Run("partial credentials fail fast", func(t *testing.T) {
		t.Setenv(operatorAdminUsernameEnv, "ops")
		t.Setenv(operatorAdminPasswordEnv, "")
		_, err := operatorAdminOptions(clock.Real())
		require.Error(t, err, "a half-configured admin plane must fail fast, not silently skip wiring")
	})
}
