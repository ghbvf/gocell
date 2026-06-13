package main

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/bootstrap"
)

// mkTopoT builds a validated multi-pod bootstrap.Topology for tests (single-pod
// is not exercised here), failing the test on an invalid combination.
func mkTopoT(t *testing.T, adapterMode, storageBackend string) bootstrap.Topology {
	t.Helper()
	topo, err := bootstrap.NewTopology(adapterMode, storageBackend, false)
	require.NoError(t, err)
	return topo
}

// app_topology_test.go covers ssobff's topology-gated multi-pod posture
// (#825 + #2017): in real/postgres (multi-pod) topology the in-memory
// idempotency claimer + service-token nonce store are insufficient, so
// NewSSOBFFApp must fail closed at startup when Redis is not configured —
// never silently degrade to in-memory (the regression #2017 fixed for
// cmd/corebundle, mirrored here).
//
// These are pure unit tests: the fail-closed gate (replaydeps.Resolve) runs
// BEFORE the PostgreSQL pool dial, so a bogus DATABASE_URL is never reached.
// The Redis claimer at-most-once behavior is covered by adapters/redis;
// transport-broker fail-closed is covered by cellmodules/eventtransport.

const (
	// 32-byte HMAC secret so newInternalAuthChain's key ring accepts it; the
	// test never reaches the internal listener, the secret only has to be valid.
	topoTestServiceSecret = "ssobff-topology-test-secret-32by" // #nosec G101 -- test fixture; never used outside unit tests
	// topoTestBogusDSN is a DSN that fails at parse time (invalid scheme/host
	// combination that pgx rejects before any network dial). Using a bare
	// "not-a-valid-dsn" string risks pgx treating it as a keyword-value DSN
	// and attempting a socket connect (~5s hang). The "://" prefix forces a URL
	// parse failure, so the pool never dials.
	topoTestBogusDSN = "://invalid-host-for-topology-test"
)

// TestSSOBFFApp_PostgresTopologyMissingRedisFailsClosed pins the #825 + nonce
// gate: real + postgres topology with no Redis env is a startup error before
// the PG pool is opened. ssobff does not route through composition.SharedDeps'
// ConsumerClaimer.Kind() check, so replaydeps.Resolve's fail-close is its only
// multi-pod safety net — this test guards it.
func TestSSOBFFApp_PostgresTopologyMissingRedisFailsClosed(t *testing.T) {
	t.Setenv("GOCELL_ADAPTER_MODE", "real")
	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "postgres")
	t.Setenv("GOCELL_REDIS_ADDR", "")
	t.Setenv("GOCELL_REDIS_CLUSTER_ADDRS", "")
	t.Setenv("GOCELL_AMQP_URL", "")

	app, err := NewSSOBFFApp(
		WithSSOBFFInternalServiceSecret(topoTestServiceSecret),
		WithSSOBFFDatabaseURL(topoTestBogusDSN),
	)

	require.Error(t, err)
	assert.Nil(t, app)
	msg := err.Error()
	// Must fail on the distributed-replay gate, NOT on the PG pool (which would
	// mean the gate ran too late / not at all).
	assert.True(t,
		strings.Contains(msg, "GOCELL_REDIS_ADDR") ||
			strings.Contains(msg, "multi-pod") ||
			strings.Contains(msg, "distributed"),
		"expected a Redis fail-closed startup error, got: %s", msg)
	assert.NotContains(t, msg, "create PG pool",
		"fail-closed gate must run before the PostgreSQL pool is opened")
	// Strengthen: verify the wrapped errcode is a recognized control-plane/Redis
	// code. replaydeps.Resolve checks Redis config before building stores; the
	// earliest fail-closed path uses ErrValidationFailed (missing GOCELL_REDIS_ADDR
	// in loadRedisConfigFromEnv), while later store/claimer build paths use
	// ErrControlplaneNonceStoreMissing / ErrControlplaneClaimerNotDistributed.
	var ecErr *errcode.Error
	if errors.As(err, &ecErr) {
		assert.True(t,
			ecErr.Code == errcode.ErrValidationFailed ||
				ecErr.Code == errcode.ErrControlplaneNonceStoreMissing ||
				ecErr.Code == errcode.ErrControlplaneClaimerNotDistributed,
			"expected a Redis/control-plane errcode, got: %s", ecErr.Code)
	}
}

// TestSSOBFFApp_DemoTopologyDoesNotRequireInfra pins that the default demo
// topology (no GOCELL_ADAPTER_MODE) keeps the in-memory transport + claimer +
// nonce store and therefore does NOT fail closed on missing Redis/broker — the
// walkthrough_test path must stay infra-free. We assert the only failure is the
// (bogus) DB pool, never a Redis/broker fail-closed error.
func TestSSOBFFApp_DemoTopologyDoesNotRequireInfra(t *testing.T) {
	t.Setenv("GOCELL_ADAPTER_MODE", "")
	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "")
	t.Setenv("GOCELL_REDIS_ADDR", "")
	t.Setenv("GOCELL_REDIS_CLUSTER_ADDRS", "")
	t.Setenv("GOCELL_AMQP_URL", "")

	app, err := NewSSOBFFApp(
		WithSSOBFFInternalServiceSecret(topoTestServiceSecret),
		WithSSOBFFDatabaseURL(topoTestBogusDSN),
	)

	require.Error(t, err, "bogus DSN should fail the PG pool in demo mode")
	assert.Nil(t, app)
	msg := err.Error()
	assert.NotContains(t, msg, "GOCELL_REDIS_ADDR",
		"demo topology must not require Redis")
	assert.NotContains(t, msg, "multi-pod",
		"demo topology must not gate on multi-pod replay backends")
	// Prove the gate fires at pool creation (bogus DSN), not before it.
	assert.Contains(t, msg, "create PG pool",
		"demo topology must reach the PG pool (bogus DSN should fail there, not at a Redis gate)")
}

// TestResolveSSOBFFBootstrapCreds pins the F1 bootstrap-credential funnel: demo
// topology uses the package-local demo constants; real topology must supply
// production credentials via GOCELL_BOOTSTRAP_ADMIN_* and fails fast on
// missing / demo-default / weak values — the public demo credentials must never
// protect a real setup/admin endpoint.
func TestResolveSSOBFFBootstrapCreds(t *testing.T) {
	t.Run("demo topology uses demo constants", func(t *testing.T) {
		creds, err := resolveSSOBFFBootstrapCreds(mkTopoT(t, "", "memory"))
		require.NoError(t, err)
		assert.Equal(t, ssobffBootstrapUsername, string(creds.Username))
		assert.Equal(t, ssobffBootstrapPassword, string(creds.Password))
	})

	t.Run("real topology missing env fails closed", func(t *testing.T) {
		t.Setenv(ssobffBootstrapAdminUserEnv, "")
		t.Setenv(ssobffBootstrapAdminPassEnv, "")
		_, err := resolveSSOBFFBootstrapCreds(mkTopoT(t, "real", "postgres"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), ssobffBootstrapAdminUserEnv)
	})

	t.Run("real topology rejects reused demo credentials", func(t *testing.T) {
		t.Setenv(ssobffBootstrapAdminUserEnv, ssobffBootstrapUsername)
		t.Setenv(ssobffBootstrapAdminPassEnv, ssobffBootstrapPassword)
		_, err := resolveSSOBFFBootstrapCreds(mkTopoT(t, "real", "postgres"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "demo")
	})

	t.Run("real topology rejects short password", func(t *testing.T) {
		t.Setenv(ssobffBootstrapAdminUserEnv, "ops-admin")
		t.Setenv(ssobffBootstrapAdminPassEnv, "short")
		_, err := resolveSSOBFFBootstrapCreds(mkTopoT(t, "real", "postgres"))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "8 bytes")
	})

	t.Run("real topology accepts valid env credentials", func(t *testing.T) {
		t.Setenv(ssobffBootstrapAdminUserEnv, "ops-admin")
		t.Setenv(ssobffBootstrapAdminPassEnv, "a-strong-prod-secret")
		creds, err := resolveSSOBFFBootstrapCreds(mkTopoT(t, "real", "postgres"))
		require.NoError(t, err)
		assert.Equal(t, "ops-admin", string(creds.Username))
		assert.Equal(t, "a-strong-prod-secret", string(creds.Password))
	})
}
