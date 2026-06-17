package composition_test

// external_blackbox_test.go — black-box (package composition_test) coverage of
// the product acceptance criterion "external composition consumers are
// fail-closed in real adapter mode" (#1410). The in-package tests
// (TestSharedDeps_Validate_ControlPlane) exercise the same logic but call the
// unexported validate() directly; this file proves the rejection from the actual
// EXTERNAL surface an out-of-tree consumer sees — composition.NewSharedDeps — built
// entirely from public constructors (no in-package fakes/helpers). The only
// example external consumer, examples/corebundlestarter, runs dev-mode, so this
// is the sole real-mode external-viewpoint coverage (#1410 review F1 round-2).

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/idempotency"
	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/auth/keystest"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/composition"
	"github.com/ghbvf/gocell/framework/runtime/eventbus"
	obmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
)

// externalRealModeDeps builds a fully-populated, real-adapter-mode (single-pod)
// composition.SharedDeps using ONLY the public package surface — exactly what an
// out-of-tree consumer has access to. mutate tweaks one field to exercise a
// specific control-plane rejection. The baseline (mutate=nil) is valid and must
// pass NewSharedDeps.
func externalRealModeDeps(t *testing.T, mutate func(*composition.SharedDeps)) composition.SharedDeps {
	t.Helper()
	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))

	topo, err := bootstrap.NewTopology("real", "postgres", true) // real, single-pod
	require.NoError(t, err)

	ks, err := keystest.MustNewKeyProvider(clk).RSAKeySet()
	require.NoError(t, err)
	issuer, err := auth.NewJWTIssuer(ks, "ext-issuer", time.Hour, clk)
	require.NoError(t, err)
	verifier, err := auth.NewJWTVerifier(ks, clk, auth.WithExpectedAudiences("ext-aud"))
	require.NoError(t, err)

	var mp kernelmetrics.Provider = kernelmetrics.NopProvider{}
	cec, err := obmetrics.NewProviderConfigEventCollector(mp)
	require.NoError(t, err)

	// 32-byte fixed test key, constructed (not a literal) so gosec G101 does not
	// flag it as a hardcoded credential — it is an in-process test fixture.
	ring, err := auth.NewHMACKeyRing(bytes.Repeat([]byte("xy"), 16), nil)
	require.NoError(t, err)
	nonce, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clk)
	require.NoError(t, err)

	eb := eventbus.New(clk)

	d := composition.SharedDeps{
		Clock:                  clk,
		Topology:               topo,
		JWTIssuer:              issuer,
		JWTVerifier:            verifier,
		MetricsProvider:        mp,
		Publisher:              eb,
		Subscriber:             eb,
		ConfigEventCollector:   cec,
		ConsumerClaimer:        idempotency.NewInMemClaimer(clk),
		InternalServiceKeyring: ring,
		NonceStore:             nonce, // single-pod in-memory is valid baseline
		InternalHTTPAddr:       "127.0.0.1:9090",
		HealthHTTPAddr:         ":9091", // non-loopback (real mode requires Pod-reachable)
	}
	// Set the token guards via field assignment (not composite-literal keys) to
	// mirror buildValidRealModeSharedDeps and avoid a gosec G101 false positive on
	// the *Token struct keys — these are in-process test guard values.
	d.MetricsToken = "ext-metrics-guard-value"
	d.VerboseToken = "ext-verbose-guard-value"
	if mutate != nil {
		mutate(&d)
	}
	return d
}

// TestExternalConsumer_RealMode_FailClosed proves an external (out-of-tree)
// composition consumer is fail-closed in real adapter mode through the public
// NewSharedDeps surface: a valid real-mode dep set is accepted, but a NoopNonceStore
// — and an in-memory store under a multi-pod topology — are rejected with the
// stable control-plane errcode. This is the external-viewpoint complement to the
// in-package TestSharedDeps_Validate_ControlPlane (#1410 review F1 round-2).
func TestExternalConsumer_RealMode_FailClosed(t *testing.T) {
	t.Run("valid real single-pod deps accepted", func(t *testing.T) {
		_, err := composition.NewSharedDeps(externalRealModeDeps(t, nil))
		require.NoError(t, err, "a fully-valid real-mode dep set must be Build-able by an external consumer")
	})

	t.Run("noop nonce store rejected fail-closed", func(t *testing.T) {
		_, err := composition.NewSharedDeps(externalRealModeDeps(t, func(d *composition.SharedDeps) {
			d.NonceStore = auth.NewNoopNonceStore()
		}))
		requireControlplaneNonceError(t, err)
	})

	t.Run("in-memory nonce store rejected for multi-pod", func(t *testing.T) {
		_, err := composition.NewSharedDeps(externalRealModeDeps(t, func(d *composition.SharedDeps) {
			topo, terr := bootstrap.NewTopology("real", "postgres", false) // multi-pod
			require.NoError(t, terr)
			d.Topology = topo // NonceStore stays the in-memory baseline → rejected
		}))
		requireControlplaneNonceError(t, err)
	})
}

// requireControlplaneNonceError asserts err carries the stable control-plane
// nonce-store errcode — the machine contract external consumers / dashboards key
// on, not just operator-facing prose.
func requireControlplaneNonceError(t *testing.T, err error) {
	t.Helper()
	require.Error(t, err)
	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr), "want an *errcode.Error from NewSharedDeps")
	assert.Equal(t, errcode.ErrControlplaneNonceStoreMissing, ecErr.Code)
}
