package composition

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	kauth "github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/idempotency"
	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/auth/keystest"
	"github.com/ghbvf/gocell/framework/runtime/bootstrap"
	"github.com/ghbvf/gocell/framework/runtime/eventbus"
	obmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
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

// testHMACRing builds an *auth.HMACKeyRing from a fixed 32-byte test secret for
// the always-on internal-listener guard (IL2).
func testHMACRing(t *testing.T) *auth.HMACKeyRing {
	t.Helper()
	ring, err := auth.NewHMACKeyRing([]byte("0123456789abcdef0123456789abcdef"), nil)
	require.NoError(t, err)
	return ring
}

// testInMemNonceStore builds a replay-safe in-memory NonceStore (Kind()==in_memory).
func testInMemNonceStore(t *testing.T, clk *clockmock.FakeClock) kauth.NonceStore {
	t.Helper()
	ns, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clk)
	require.NoError(t, err)
	return ns
}

// buildTestSharedDeps creates a dev-mode SharedDeps with all validate()-required
// fields populated — including the always-on internal-listener guard
// (InternalHTTPAddr + InternalServiceKeyring) and the verbose-endpoint waiver
// (VerboseDisabled) — using in-memory / fake implementations. The control-plane
// production checks are skipped in dev adapter mode; buildValidRealModeSharedDeps
// flips the topology and the fields those checks require.
func buildTestSharedDeps(t *testing.T) *SharedDeps {
	t.Helper()

	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))
	var mp kernelmetrics.Provider = kernelmetrics.NopProvider{}

	cec, err := obmetrics.NewProviderConfigEventCollector(mp)
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
		Publisher:              eb,
		Subscriber:             eb,
		ConfigEventCollector:   cec,
		ConsumerClaimer:        claimer,
		InternalServiceKeyring: testHMACRing(t),
		NonceStore:             testInMemNonceStore(t, clk),
		InternalHTTPAddr:       "127.0.0.1:9090",
		VerboseDisabled:        true,
	})
	require.NoError(t, err)
	return s
}

// buildValidRealModeSharedDeps returns a SharedDeps that passes every control-plane
// production check: real adapter mode, single-pod (so an in-memory nonce store and
// in-memory claimer are accepted), token-gated verbose + metrics, and a
// Pod-reachable health address. Negative control-plane tests start from this
// baseline and break exactly one field.
func buildValidRealModeSharedDeps(t *testing.T) *SharedDeps {
	t.Helper()
	s := buildTestSharedDeps(t)
	topo, err := bootstrap.NewTopology("real", "postgres", true) // single-pod
	require.NoError(t, err)
	s.Topology = topo
	s.VerboseDisabled = false
	s.VerboseToken = "prod-verbose-token"
	s.MetricsToken = "prod-metrics-token"
	s.HealthHTTPAddr = ":9091" // non-loopback
	require.NoError(t, s.validate(), "real-mode baseline dep set must be valid")
	return s
}

// fakeDistributedClaimer reports ClaimerKindDistributed while delegating Claim to
// an embedded in-memory claimer — used to isolate the CP7 nonce-store check from
// the CP8 claimer check in multi-pod negative tests.
type fakeDistributedClaimer struct{ *idempotency.InMemClaimer }

func (fakeDistributedClaimer) Kind() idempotency.ClaimerKind {
	return idempotency.ClaimerKindDistributed
}

// fakeDistributedNonceStore reports NonceStoreKindDistributed while delegating to
// an embedded store — used to isolate the CP8 claimer check from CP6/CP7.
type fakeDistributedNonceStore struct{ *auth.InMemoryNonceStore }

func (fakeDistributedNonceStore) Kind() kauth.NonceStoreKind {
	return kauth.NonceStoreKindDistributed
}

// fakeUnknownKindNonceStore reports an unrecognized NonceStoreKind while
// delegating to an embedded in-memory store — used to exercise the fail-closed
// default branch of validateProductionNonceStore (#1410 review F2). Its very
// existence is also the rebuttal to "a method on the store cannot lie": Kind()
// is an implementation self-report, so it can return any value, recognized or
// not — the validator must therefore fail-closed on the unknown set.
type fakeUnknownKindNonceStore struct{ *auth.InMemoryNonceStore }

func (fakeUnknownKindNonceStore) Kind() kauth.NonceStoreKind {
	return kauth.NonceStoreKind("totally-bogus-kind")
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
			s := buildValidRealModeSharedDeps(t)
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
	// wantField asserts the error names the specific missing field (validate uses
	// errors.Join, not short-circuit, so each field is validated INDEPENDENTLY —
	// nilling exactly one from a valid base must surface that one's name). This
	// proves Publisher and Subscriber are each guarded on their own, not merely
	// that "some" field check fired.
	tests := []struct {
		name      string
		mutFn     func(s *SharedDeps)
		wantField string
	}{
		{
			name:      "Clock nil",
			mutFn:     func(s *SharedDeps) { s.Clock = nil },
			wantField: "Clock",
		},
		{
			name:      "JWTIssuer nil",
			mutFn:     func(s *SharedDeps) { s.JWTIssuer = nil },
			wantField: "JWTIssuer",
		},
		{
			name:      "JWTVerifier nil",
			mutFn:     func(s *SharedDeps) { s.JWTVerifier = nil },
			wantField: "JWTVerifier",
		},
		{
			name:      "MetricsProvider nil",
			mutFn:     func(s *SharedDeps) { s.MetricsProvider = nil },
			wantField: "MetricsProvider",
		},
		{
			name:      "Publisher nil",
			mutFn:     func(s *SharedDeps) { s.Publisher = nil },
			wantField: "Publisher",
		},
		{
			name:      "Subscriber nil",
			mutFn:     func(s *SharedDeps) { s.Subscriber = nil },
			wantField: "Subscriber",
		},
		{
			name:      "ConfigEventCollector nil",
			mutFn:     func(s *SharedDeps) { s.ConfigEventCollector = nil },
			wantField: "ConfigEventCollector",
		},
		{
			name:      "ConsumerClaimer nil",
			mutFn:     func(s *SharedDeps) { s.ConsumerClaimer = nil },
			wantField: "ConsumerClaimer",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := buildTestSharedDeps(t)
			tc.mutFn(s)
			err := s.validate()
			require.Error(t, err, "expected error for %s", tc.name)
			assert.Contains(t, err.Error(), tc.wantField,
				"validate() error must name the missing field %q", tc.wantField)
		})
	}
}

// realMultiPodTopo builds a real adapter-mode, multi-pod topology
// (requiresDistributedReplay == true).
func realMultiPodTopo(t *testing.T) bootstrap.Topology {
	t.Helper()
	topo, err := bootstrap.NewTopology("real", "postgres", false) // real adapter mode, multi-pod
	require.NoError(t, err)
	return topo
}

// TestSharedDeps_Validate_ControlPlane covers the control-plane production
// guards moved from cmd/corebundle into composition.validate (#1410): the
// always-on internal-listener guard + verbose-endpoint gating (every adapter
// mode) and the real-adapter-mode token / nonce-store-kind / claimer-kind
// checks. Every consumer of NewSharedDeps inherits these, so an external
// composition consumer is fail-closed without reusing any cmd-private type.
func TestSharedDeps_Validate_ControlPlane(t *testing.T) {
	clk := clockmock.New(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC))

	tests := []struct {
		name    string
		base    func(t *testing.T) *SharedDeps
		mutate  func(t *testing.T, s *SharedDeps)
		wantErr bool
		errSub  string
		// errCode, when set, pins the *errcode.Error.Code carried by the failure.
		// The error string (errSub) is operator-facing prose that may be reworded;
		// the code is the machine contract dashboards / runbooks key on, so the
		// control-plane sentinels are asserted by code, not just substring
		// (#1410 review F8). Left empty for cases whose failure joins more than one
		// error (no single canonical code).
		errCode errcode.Code
	}{
		// --- always-on (dev baseline) ---
		{name: "dev baseline valid", base: buildTestSharedDeps},
		{
			name:    "IL1 internal addr empty rejected in every mode",
			base:    buildTestSharedDeps,
			mutate:  func(_ *testing.T, s *SharedDeps) { s.InternalHTTPAddr = "" },
			wantErr: true, errSub: "InternalHTTPAddr", errCode: errcode.ErrValidationFailed,
		},
		{
			name:    "IL2 internal HMAC ring nil rejected in every mode",
			base:    buildTestSharedDeps,
			mutate:  func(_ *testing.T, s *SharedDeps) { s.InternalServiceKeyring = nil },
			wantErr: true, errSub: "InternalServiceKeyring", errCode: errcode.ErrControlplaneServiceSecretMissing,
		},
		{
			name:    "V2 verbose unconfigured rejected in every mode",
			base:    buildTestSharedDeps,
			mutate:  func(_ *testing.T, s *SharedDeps) { s.VerboseDisabled = false; s.VerboseToken = "" },
			wantErr: true, errSub: "GOCELL_READYZ_VERBOSE_TOKEN", errCode: errcode.ErrControlplaneVerboseTokenMissing,
		},
		{
			name:   "V2 verbose token configured accepted",
			base:   buildTestSharedDeps,
			mutate: func(_ *testing.T, s *SharedDeps) { s.VerboseDisabled = false; s.VerboseToken = "tok" },
		},
		// --- real-adapter-mode (real single-pod baseline) ---
		{name: "real baseline valid", base: buildValidRealModeSharedDeps},
		{
			name:    "CP1 verbose-disabled forbidden in real mode",
			base:    buildValidRealModeSharedDeps,
			mutate:  func(_ *testing.T, s *SharedDeps) { s.VerboseDisabled = true },
			wantErr: true, errSub: "GOCELL_READYZ_VERBOSE_DISABLED", errCode: errcode.ErrControlplaneVerboseTokenMissing,
		},
		{
			name:    "CP3 metrics token required in real mode",
			base:    buildValidRealModeSharedDeps,
			mutate:  func(_ *testing.T, s *SharedDeps) { s.MetricsToken = "" },
			wantErr: true, errSub: "GOCELL_METRICS_TOKEN", errCode: errcode.ErrValidationFailed,
		},
		{
			name:    "CP5 nonce store required in real mode",
			base:    buildValidRealModeSharedDeps,
			mutate:  func(_ *testing.T, s *SharedDeps) { s.NonceStore = nil },
			wantErr: true, errSub: "NonceStore must be set", errCode: errcode.ErrControlplaneNonceStoreMissing,
		},
		{
			name:    "CP6 noop nonce store rejected in real mode",
			base:    buildValidRealModeSharedDeps,
			mutate:  func(_ *testing.T, s *SharedDeps) { s.NonceStore = auth.NewNoopNonceStore() },
			wantErr: true, errSub: "NoopNonceStore", errCode: errcode.ErrControlplaneNonceStoreMissing,
		},
		{
			name: "CP7 in-memory nonce store rejected for real multi-pod",
			base: buildValidRealModeSharedDeps,
			mutate: func(t *testing.T, s *SharedDeps) {
				s.Topology = realMultiPodTopo(t)
				// distributed claimer so only the CP7 nonce check fires.
				s.ConsumerClaimer = fakeDistributedClaimer{idempotency.NewInMemClaimer(clk)}
			},
			wantErr: true, errSub: "in-memory nonce store requires", errCode: errcode.ErrControlplaneNonceStoreMissing,
		},
		{
			name: "F2 unrecognized nonce store kind rejected fail-closed in real mode",
			base: buildValidRealModeSharedDeps,
			mutate: func(t *testing.T, s *SharedDeps) {
				// Single-pod real mode isolates the fail-closed default branch: an
				// in-memory claimer is accepted single-pod, so only the unknown
				// nonce-store kind fails. Pre-fix this passed (default accepted any
				// non-noop/in-memory kind) — fail-open (#1410 review F2).
				ns, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clk)
				require.NoError(t, err)
				s.NonceStore = fakeUnknownKindNonceStore{ns}
			},
			wantErr: true, errSub: "unrecognized kind", errCode: errcode.ErrControlplaneNonceStoreMissing,
		},
		{
			name: "CP8 in-memory claimer rejected for real multi-pod",
			base: buildValidRealModeSharedDeps,
			mutate: func(t *testing.T, s *SharedDeps) {
				s.Topology = realMultiPodTopo(t)
				// distributed nonce store so only the CP8 claimer check fires.
				ns, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clk)
				require.NoError(t, err)
				s.NonceStore = fakeDistributedNonceStore{ns}
			},
			wantErr: true, errSub: "ClaimerKindDistributed", errCode: errcode.ErrControlplaneClaimerNotDistributed,
		},
		{
			name: "real multi-pod with distributed nonce + claimer accepted",
			base: buildValidRealModeSharedDeps,
			mutate: func(t *testing.T, s *SharedDeps) {
				s.Topology = realMultiPodTopo(t)
				ns, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clk)
				require.NoError(t, err)
				s.NonceStore = fakeDistributedNonceStore{ns}
				s.ConsumerClaimer = fakeDistributedClaimer{idempotency.NewInMemClaimer(clk)}
			},
		},
		// --- additional coverage cases ---
		{
			name: "V1 both-set warn-but-pass (VerboseDisabled + VerboseToken)",
			base: buildTestSharedDeps,
			mutate: func(_ *testing.T, s *SharedDeps) {
				s.VerboseDisabled = true
				s.VerboseToken = "tok"
			},
		},
		{
			name: "CP7-pass real single-pod with in-memory nonce accepted",
			base: buildValidRealModeSharedDeps,
			// baseline is already single-pod + in-memory nonce; this case makes it explicit
		},
		{
			name: "nil ConsumerClaimer with real multi-pod fails closed",
			base: buildValidRealModeSharedDeps,
			mutate: func(t *testing.T, s *SharedDeps) {
				s.Topology = realMultiPodTopo(t)
				s.ConsumerClaimer = nil
			},
			wantErr: true, errSub: "ConsumerClaimer",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			s := tc.base(t)
			if tc.mutate != nil {
				tc.mutate(t, s)
			}
			err := s.validate()
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			if tc.errSub != "" {
				assert.Contains(t, err.Error(), tc.errSub)
			}
			if tc.errCode != "" {
				var ecErr *errcode.Error
				require.True(t, errors.As(err, &ecErr),
					"want an *errcode.Error in the chain to pin Code")
				assert.Equal(t, tc.errCode, ecErr.Code,
					"control-plane failure must carry the stable errcode (machine contract), "+
						"not just operator-facing prose")
			}
		})
	}
}
