package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
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

// TestNewSSOBFFJWT pins the #2052 multi-pod JWT key gate: ssobff's JWT signing
// keys are topology-gated via cellsecrets.LoadKeySet (mirroring cmd/corebundle's
// buildJWTDeps), not a per-process ephemeral key pair. In real adapter mode the
// shared key env vars (GOCELL_JWT_PRIVATE_KEY / GOCELL_JWT_PUBLIC_KEY) are
// required and a missing pair fails closed — a per-pod ephemeral key would make
// replica A's tokens 401 against replica B (the #2052 bug, 4th sibling of the
// #825/#2017 single-pod-primitive family). Demo topology keeps ephemeral keys so
// the walkthrough path stays infra-free.
func TestNewSSOBFFJWT(t *testing.T) {
	clk := clock.Real()

	t.Run("demo topology generates ephemeral keys", func(t *testing.T) {
		t.Setenv(auth.EnvJWTPrivateKey, "")
		t.Setenv(auth.EnvJWTPublicKey, "")
		issuer, verifier, err := newSSOBFFJWT(mkTopoT(t, "", "memory"), clk)
		require.NoError(t, err)
		assert.NotNil(t, issuer)
		assert.NotNil(t, verifier)
	})

	t.Run("real topology missing JWT key env fails closed", func(t *testing.T) {
		t.Setenv(auth.EnvJWTPrivateKey, "")
		t.Setenv(auth.EnvJWTPublicKey, "")
		_, _, err := newSSOBFFJWT(mkTopoT(t, "real", "postgres"), clk)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "JWT key",
			"real topology must fail closed on missing shared JWT key env (no per-pod ephemeral)")
		// Strengthen beyond the message substring: the wrapped errcode must be the
		// key-missing code, so a future message reword cannot silently pass the gate.
		var ecErr *errcode.Error
		require.ErrorAs(t, err, &ecErr)
		assert.Equal(t, auth.ErrKeyMissing, ecErr.Code,
			"real-mode missing JWT key must surface auth.ErrKeyMissing")
	})

	t.Run("real shared key: replica A issues, replica B verifies (#2052)", func(t *testing.T) {
		// The #2052 user-visible failure: replica A signs a token, replica B
		// verifies it. With per-pod ephemeral keys, B 401s A's token; with the
		// shared env key pair both pods derive the same KeySet, so cross-pod
		// verify succeeds. Asserts the real cross-replica behavior, not just that
		// construction returned non-nil — guards issuer/audience/key wiring.
		setSSOBFFTestJWTKeyEnv(t)
		realTopo := mkTopoT(t, "real", "postgres")

		issuerA, _, err := newSSOBFFJWT(realTopo, clk)
		require.NoError(t, err, "replica A: build JWT issuer/verifier")
		_, verifierB, err := newSSOBFFJWT(realTopo, clk)
		require.NoError(t, err, "replica B: build JWT issuer/verifier")

		const subject = "user-2052"
		token, err := issuerA.Issue(auth.TokenIntentAccess, subject, auth.IssueOptions{
			Audience: []string{ssobffJWTAudience},
		})
		require.NoError(t, err, "replica A: issue access token")

		claims, err := verifierB.VerifyIntent(context.Background(), token, auth.TokenIntentAccess)
		require.NoError(t, err, "replica B must verify replica A's token (cross-pod, shared key)")
		assert.Equal(t, subject, claims.Subject, "subject must round-trip across replicas")
		assert.Equal(t, auth.TokenIntentAccess, claims.TokenUse, "token intent preserved")
	})
}

// setSSOBFFTestJWTKeyEnv installs a PEM-encoded RSA key pair into the shared JWT
// key env vars so a real-topology newSSOBFFJWT call loads them rather than failing
// closed. Mirrors cmd/corebundle's setTestJWTKeyEnv. (auth.GenerateRSAKeyPair is
// permitted here: GENERATE-RSA-KEYPAIR-FUNNEL-01 scans production files only,
// _test.go is excluded.)
func setSSOBFFTestJWTKeyEnv(t *testing.T) {
	t.Helper()
	priv, pub, err := auth.GenerateRSAKeyPair()
	require.NoError(t, err, "generate test RSA key pair")
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
