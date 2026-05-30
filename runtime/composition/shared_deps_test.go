package composition

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/keystest"
	"github.com/ghbvf/gocell/runtime/eventbus"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

// buildTestJWTPair builds a *auth.JWTIssuer and *auth.JWTVerifier using
// ephemeral RSA test keys. Requires a non-nil clock.
func buildTestJWTPair(t *testing.T, clk *clockmock.FakeClock) (*auth.JWTIssuer, *auth.JWTVerifier) {
	t.Helper()
	kp := keystest.MustNewKeyProvider(clk)
	ks, err := kp.RSAKeySet()
	require.NoError(t, err)
	issuer, err := auth.NewJWTIssuer(ks, "test-issuer", time.Hour, clk)
	require.NoError(t, err)
	verifier, err := auth.NewJWTVerifier(ks, clk, auth.WithExpectedAudiences("test-audience"))
	require.NoError(t, err)
	return issuer, verifier
}

// buildTestSharedDeps creates a SharedDeps with all Validate()-required fields
// populated using in-memory / fake implementations.
func buildTestSharedDeps(t *testing.T) *SharedDeps {
	t.Helper()

	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	var mp kernelmetrics.Provider = kernelmetrics.NopProvider{}

	cec, err := obmetrics.NewProviderConfigEventCollector(mp)
	require.NoError(t, err)

	ebc, err := obmetrics.NewProviderEventbusCacheCollector(mp)
	require.NoError(t, err)

	eb := eventbus.New(clk)
	claimer := idempotency.NewInMemClaimer(clk)
	issuer, verifier := buildTestJWTPair(t, clk)

	return &SharedDeps{
		Clock:                  clk,
		JWTIssuer:              issuer,
		JWTVerifier:            verifier,
		MetricsProvider:        mp,
		EventBus:               eb,
		ConfigEventCollector:   cec,
		EventbusCacheCollector: ebc,
		ConsumerClaimer:        claimer,
	}
}

// minimalSharedDeps is a helper alias used by builder_test.go.
func minimalSharedDeps(t *testing.T) *SharedDeps {
	t.Helper()
	return buildTestSharedDeps(t)
}

func TestSharedDeps_Validate_ValidDeps(t *testing.T) {
	s := buildTestSharedDeps(t)
	require.NoError(t, s.Validate())
}

func TestSharedDeps_Validate_NilReceiver(t *testing.T) {
	var s *SharedDeps
	err := s.Validate()
	require.Error(t, err)
}

// TestSharedDeps_Validate_MissingFields verifies that each Validate()-required
// field, when nil, causes Validate() to return an error.
func TestSharedDeps_Validate_MissingFields(t *testing.T) {
	tests := []struct {
		name  string
		mutFn func(s *SharedDeps)
	}{
		{
			name:  "Clock nil",
			mutFn: func(s *SharedDeps) { s.Clock = nil },
		},
		{
			name:  "JWTIssuer nil",
			mutFn: func(s *SharedDeps) { s.JWTIssuer = nil },
		},
		{
			name:  "JWTVerifier nil",
			mutFn: func(s *SharedDeps) { s.JWTVerifier = nil },
		},
		{
			name:  "MetricsProvider nil",
			mutFn: func(s *SharedDeps) { s.MetricsProvider = nil },
		},
		{
			name:  "EventBus nil",
			mutFn: func(s *SharedDeps) { s.EventBus = nil },
		},
		{
			name:  "ConfigEventCollector nil",
			mutFn: func(s *SharedDeps) { s.ConfigEventCollector = nil },
		},
		{
			name:  "EventbusCacheCollector nil",
			mutFn: func(s *SharedDeps) { s.EventbusCacheCollector = nil },
		},
		{
			name:  "ConsumerClaimer nil",
			mutFn: func(s *SharedDeps) { s.ConsumerClaimer = nil },
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := buildTestSharedDeps(t)
			tc.mutFn(s)
			err := s.Validate()
			assert.Error(t, err, "expected error for %s", tc.name)
		})
	}
}
