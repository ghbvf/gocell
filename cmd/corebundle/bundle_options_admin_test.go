package main

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/assembly"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
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

// TestDefaultRuntimeOptions_OperatorAdminFinalAssembly closes the #1755 F4 gap the
// helper-only test left open: TestOperatorAdminOptions proves operatorAdminOptions
// returns a coupled pair, but NOT that defaultRuntimeOptions actually routes through
// it and appends the result. This drives the REAL defaultRuntimeOptions (minimal
// valid inputs, mirroring TestDefaultRuntimeOptions_IncludesRedisHealthAndCloser) and
// asserts the operator-credential delta is exactly 2 options (AdminListener + audit
// chain verify endpoint) — so dropping `opts = append(opts, adminOpts...)`, or routing
// around the helper, turns this red. The half-configured case proves the fail-fast
// propagates out of the final assembly rather than being silently swallowed.
func TestDefaultRuntimeOptions_OperatorAdminFinalAssembly(t *testing.T) {
	shared, locals := buildTestSharedDepsAndLocals(t)
	asm := assembly.New(clock.Real(), assembly.Config{ID: "test-operator-admin", DurabilityMode: outbox.DurabilityDemo})
	cb, err := buildConsumerBase(shared)
	require.NoError(t, err)
	build := func() ([]bootstrap.Option, error) {
		return defaultRuntimeOptions(shared, locals, asm, cb, http.NewServeMux(), adapterInfoForSharedDeps(shared, locals))
	}

	// Baseline: no operator credentials → admin plane disabled. The final assembly
	// must NOT register an AdminListener.
	t.Setenv(operatorAdminUsernameEnv, "")
	t.Setenv(operatorAdminPasswordEnv, "")
	baseOpts, err := build()
	require.NoError(t, err)
	baseB := newBootstrapFromOptions(shared.Clock, baseOpts)
	assert.NotContains(t, baseB.ConfiguredListeners(), cell.AdminListener,
		"no operator credentials must leave the AdminListener unregistered in the final assembly")

	// Both credentials present → defaultRuntimeOptions must register the AdminListener
	// (IDENTITY assertion: a dropped/bypassed `opts = append(opts, adminOpts...)` or a
	// rewiring that omits the AdminListener turns this red) AND add exactly 2 options
	// (AdminListener + audit chain verify endpoint — delta as defense-in-depth).
	t.Setenv(operatorAdminUsernameEnv, "ops")
	t.Setenv(operatorAdminPasswordEnv, "s3cret-operator-pw")
	t.Setenv(adminHTTPAddrEnv, "127.0.0.1:19092")
	adminOpts, err := build()
	require.NoError(t, err)
	adminB := newBootstrapFromOptions(shared.Clock, adminOpts)
	assert.Contains(t, adminB.ConfiguredListeners(), cell.AdminListener,
		"operator credentials must register cell.AdminListener through defaultRuntimeOptions (final assembly)")
	assert.Len(t, adminOpts, len(baseOpts)+2,
		"operator credentials add exactly the AdminListener + audit chain verify endpoint (delta defense-in-depth)")

	// Half-configured credentials must fail the WHOLE assembly (fail-fast propagates),
	// not silently skip the admin plane.
	t.Setenv(operatorAdminPasswordEnv, "")
	_, err = build()
	require.Error(t, err, "half-configured operator credentials must fail defaultRuntimeOptions")
}
