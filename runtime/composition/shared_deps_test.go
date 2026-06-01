package composition

import (
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/idempotency"
	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/keystest"
	"github.com/ghbvf/gocell/runtime/bootstrap"
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

	topo, err := bootstrap.NewTopology("", "memory", false)
	require.NoError(t, err)

	s, err := NewSharedDeps(SharedDeps{
		Clock:                  clk,
		Topology:               topo,
		JWTIssuer:              issuer,
		JWTVerifier:            verifier,
		MetricsProvider:        mp,
		EventBus:               eb,
		ConfigEventCollector:   cec,
		EventbusCacheCollector: ebc,
		ConsumerClaimer:        claimer,
	})
	require.NoError(t, err)
	return s
}

// minimalSharedDeps is a helper alias used by builder_test.go.
func minimalSharedDeps(t *testing.T) *SharedDeps {
	t.Helper()
	return buildTestSharedDeps(t)
}

func TestSharedDeps_Validate_ValidDeps(t *testing.T) {
	s := buildTestSharedDeps(t)
	require.NoError(t, s.validate())
}

// TestSharedDeps_SealedMarker_Unexported asserts the validity marker is an
// unexported field so a package-external literal cannot stamp it — the
// sealed-construction invariant Build relies on. The package-internal blind spot
// (an in-package second constructor forgetting to stamp) is a Go package-
// visibility ceiling; this reflect freeze + TestBuilder_UnsealedSharedDeps_Rejected
// are its Medium backstop. Upstream Hard-ization (if feasible) tracked in gh #1412.
func TestSharedDeps_SealedMarker_Unexported(t *testing.T) {
	rt := reflect.TypeOf(SharedDeps{})
	f, ok := rt.FieldByName("valid")
	require.True(t, ok, "SharedDeps must retain the 'valid' sealed-construction marker")
	assert.False(t, f.IsExported(),
		"SharedDeps.valid must be unexported so only NewSharedDeps can set it")
}

// TestNewSharedDeps_StampsAndValidates verifies NewSharedDeps rejects a missing
// required field and stamps a valid dep set on success.
func TestNewSharedDeps_StampsAndValidates(t *testing.T) {
	s := buildTestSharedDeps(t)
	// buildTestSharedDeps already routed through NewSharedDeps; the returned
	// instance must satisfy validate and carry the marker.
	require.NoError(t, s.validate())
	assert.True(t, s.valid)

	// Missing Topology (zero value) is rejected.
	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	issuer, verifier := buildTestJWTPair(t, clk)
	_, err := NewSharedDeps(SharedDeps{
		Clock:       clk,
		JWTIssuer:   issuer,
		JWTVerifier: verifier,
	})
	require.Error(t, err)
}

// TestSharedDeps_Validate_RealModeRejectsLoopbackHealthAddr verifies that the
// health-listener reachability guard — moved from cmd/corebundle to
// composition.validate (#1085 follow-up) so external consumers inherit it —
// rejects a loopback HealthHTTPAddr in production adapter mode unless
// HealthLocalOnly waives it.
func TestSharedDeps_Validate_RealModeRejectsLoopbackHealthAddr(t *testing.T) {
	prodTopo, err := bootstrap.NewTopology("real", "postgres", true)
	require.NoError(t, err)

	tests := []struct {
		name            string
		addr            string
		healthLocalOnly bool
		wantErr         bool
	}{
		{name: "loopback rejected", addr: "127.0.0.1:9091", wantErr: true},
		{name: "localhost rejected", addr: "localhost:9091", wantErr: true},
		{name: "ipv6 loopback rejected", addr: "[::1]:9091", wantErr: true},
		{name: "wildcard accepted", addr: ":9091"},
		{name: "zero wildcard accepted", addr: "0.0.0.0:9091"},
		{name: "pod reachable accepted", addr: "10.0.0.12:9091"},
		{name: "loopback accepted with explicit local-only waiver", addr: "127.0.0.1:9091", healthLocalOnly: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := buildTestSharedDeps(t)
			s.Topology = prodTopo
			s.HealthHTTPAddr = tc.addr
			s.HealthLocalOnly = tc.healthLocalOnly

			err := s.validate()
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), "GOCELL_HTTP_HEALTH_ADDR")
			assert.Contains(t, err.Error(), "GOCELL_HTTP_HEALTH_LOCAL_ONLY=1")
		})
	}
}

// TestIsLoopbackBindAddr table-drives the address parser used by
// validateHealthReachability.
func TestIsLoopbackBindAddr(t *testing.T) {
	cases := []struct {
		name string
		addr string
		want bool
	}{
		{"ipv4 loopback with port", "127.0.0.1:8080", true},
		{"ipv4 loopback bare", "127.0.0.1", true},
		{"ipv4 loopback alt range", "127.0.0.5:9090", true},
		{"ipv6 loopback bracketed", "[::1]:8080", true},
		{"ipv6 loopback bracketed bare", "[::1]", true},
		{"ipv6 loopback unbracketed", "::1", true},
		{"hostname localhost lowercase", "localhost:9090", true},
		{"hostname localhost mixed case", "LocalHost:9090", true},
		{"port-only colon means all interfaces", ":8080", false},
		{"empty string", "", false},
		{"public ipv4", "8.8.8.8:80", false},
		{"private ipv4", "10.0.0.5:8080", false},
		{"unspecified ipv4", "0.0.0.0:8080", false},
		{"unspecified ipv6", "[::]:8080", false},
		{"bare hostname not localhost", "example.com:443", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := isLoopbackBindAddr(tc.addr)
			assert.Equal(t, tc.want, got, "addr=%q", tc.addr)
		})
	}
}

func TestSharedDeps_Validate_NilReceiver(t *testing.T) {
	var s *SharedDeps
	err := s.validate()
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
			err := s.validate()
			assert.Error(t, err, "expected error for %s", tc.name)
		})
	}
}
