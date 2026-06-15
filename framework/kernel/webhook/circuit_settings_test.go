package webhook

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
)

// TestCircuitBreakerSettings_Defaults verifies that defaultCircuitBreakerSettings
// returns values equal to the original package-const values (TripThreshold=5,
// OpenTimeout=60s, HalfOpenProbes=1).
func TestCircuitBreakerSettings_Defaults(t *testing.T) {
	s := defaultCircuitBreakerSettings()
	assert.Equal(t, 5, s.TripThreshold, "default TripThreshold must equal original const 5")
	assert.Equal(t, 60*time.Second, s.OpenTimeout, "default OpenTimeout must equal original const 60s")
	assert.Equal(t, 1, s.HalfOpenProbes, "default HalfOpenProbes must equal original const 1")
}

// TestCircuitBreakerSettings_Validate_NonPositiveTrip verifies that a
// TripThreshold ≤ 0 is rejected (fail-fast).
func TestCircuitBreakerSettings_Validate_NonPositiveTrip(t *testing.T) {
	cases := []struct {
		name string
		s    CircuitBreakerSettings
	}{
		{"zero trip", CircuitBreakerSettings{TripThreshold: 0, OpenTimeout: time.Second, HalfOpenProbes: 1}},
		{"negative trip", CircuitBreakerSettings{TripThreshold: -1, OpenTimeout: time.Second, HalfOpenProbes: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.s.Validate()
			require.Error(t, err, "non-positive TripThreshold must be rejected")
			assert.Contains(t, err.Error(), "TripThreshold")
		})
	}
}

// TestCircuitBreakerSettings_Validate_NonPositiveOpenTimeout verifies that
// OpenTimeout ≤ 0 is rejected (fail-fast).
func TestCircuitBreakerSettings_Validate_NonPositiveOpenTimeout(t *testing.T) {
	cases := []struct {
		name string
		s    CircuitBreakerSettings
	}{
		{"zero timeout", CircuitBreakerSettings{TripThreshold: 1, OpenTimeout: 0, HalfOpenProbes: 1}},
		{"negative timeout", CircuitBreakerSettings{TripThreshold: 1, OpenTimeout: -time.Second, HalfOpenProbes: 1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.s.Validate()
			require.Error(t, err, "non-positive OpenTimeout must be rejected")
			assert.Contains(t, err.Error(), "OpenTimeout")
		})
	}
}

// TestCircuitBreakerSettings_Validate_NonPositiveHalfOpenProbes verifies that
// HalfOpenProbes ≤ 0 is rejected (fail-fast).
func TestCircuitBreakerSettings_Validate_NonPositiveHalfOpenProbes(t *testing.T) {
	cases := []struct {
		name string
		s    CircuitBreakerSettings
	}{
		{"zero probes", CircuitBreakerSettings{TripThreshold: 1, OpenTimeout: time.Second, HalfOpenProbes: 0}},
		{"negative probes", CircuitBreakerSettings{TripThreshold: 1, OpenTimeout: time.Second, HalfOpenProbes: -1}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.s.Validate()
			require.Error(t, err, "non-positive HalfOpenProbes must be rejected")
			assert.Contains(t, err.Error(), "HalfOpenProbes")
		})
	}
}

// TestCircuitBreakerSettings_Validate_Valid verifies that a valid settings struct
// passes validation without error.
func TestCircuitBreakerSettings_Validate_Valid(t *testing.T) {
	s := CircuitBreakerSettings{TripThreshold: 3, OpenTimeout: 30 * time.Second, HalfOpenProbes: 2}
	require.NoError(t, s.Validate())
}

// TestWithCircuitBreakerSettings_InvalidSettings verifies that NewDispatcher
// returns an error when provided settings fail validation (fail-fast at
// construction time, not first delivery).
func TestWithCircuitBreakerSettings_InvalidSettings(t *testing.T) {
	cases := []struct {
		name string
		s    CircuitBreakerSettings
		want string
	}{
		{
			"zero TripThreshold",
			CircuitBreakerSettings{TripThreshold: 0, OpenTimeout: time.Second, HalfOpenProbes: 1},
			"TripThreshold",
		},
		{
			"zero OpenTimeout",
			CircuitBreakerSettings{TripThreshold: 1, OpenTimeout: 0, HalfOpenProbes: 1},
			"OpenTimeout",
		},
		{
			"zero HalfOpenProbes",
			CircuitBreakerSettings{TripThreshold: 1, OpenTimeout: time.Second, HalfOpenProbes: 0},
			"HalfOpenProbes",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewDispatcher(
				clockmock.New(time.Unix(dispatchTestTS, 0)),
				dispatchTestSigner(t),
				NewSafePolicy(WithAllowLoopback()),
				staticSelector("http://example.test/"),
				WithCircuitBreakerSettings(tc.s),
			)
			require.Error(t, err, "invalid settings must cause NewDispatcher to return error")
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestWithCircuitBreakerSettings_CustomTripThreshold verifies that a Dispatcher
// built with TripThreshold=2 trips after exactly 3 consecutive failures (> 2),
// not after the default 5.
func TestWithCircuitBreakerSettings_CustomTripThreshold(t *testing.T) {
	srv, hits, _ := countingServer(t, http.StatusInternalServerError)

	customSettings := CircuitBreakerSettings{
		TripThreshold:  2,
		OpenTimeout:    30 * time.Second,
		HalfOpenProbes: 1,
	}
	d, err := NewDispatcher(
		clockmock.New(time.Unix(dispatchTestTS, 0)),
		dispatchTestSigner(t),
		NewSafePolicy(WithAllowLoopback()),
		staticSelector(srv.URL),
		WithCircuitBreakerSettings(customSettings),
	)
	require.NoError(t, err)

	// Deliver exactly TripThreshold times — breaker must still be CLOSED.
	for i := range customSettings.TripThreshold {
		res := d.Handle(context.Background(), newTestEntry(t, []byte(`{}`)))
		assert.Equal(t, outbox.DispositionRequeue, res.Disposition,
			"delivery %d: circuit must be closed (only %d failures so far)", i+1, i+1)
	}
	assert.Equal(t, int32(customSettings.TripThreshold), hits.Load(), //nolint:gosec // TripThreshold is validated > 0 and << MaxInt32
		"all TripThreshold deliveries must have reached the endpoint")

	// One more exceeds the threshold → trips the circuit.
	d.Handle(context.Background(), newTestEntry(t, []byte(`{}`)))
	// The NEXT call must be fast-failed (circuit open).
	requireCircuitOpen(t, d.Handle(context.Background(), newTestEntry(t, []byte(`{}`))))
}

// TestWithCircuitBreakerSettings_CustomOpenTimeout verifies that the custom
// OpenTimeout controls when the breaker admits a half-open probe: advancing less
// than the timeout keeps the circuit open; advancing past it allows a probe.
func TestWithCircuitBreakerSettings_CustomOpenTimeout(t *testing.T) {
	srv, _, _ := countingServer(t, http.StatusInternalServerError)

	customSettings := CircuitBreakerSettings{
		TripThreshold:  2,
		OpenTimeout:    10 * time.Second,
		HalfOpenProbes: 1,
	}
	fc := clockmock.New(time.Unix(dispatchTestTS, 0))
	d, err := NewDispatcher(fc, dispatchTestSigner(t),
		NewSafePolicy(WithAllowLoopback()), staticSelector(srv.URL),
		WithCircuitBreakerSettings(customSettings))
	require.NoError(t, err)

	// Trip the circuit.
	for range customSettings.TripThreshold + 1 {
		d.Handle(context.Background(), newTestEntry(t, []byte(`{}`)))
	}
	requireCircuitOpen(t, d.Handle(context.Background(), newTestEntry(t, []byte(`{}`))))

	// Advancing less than OpenTimeout → still open.
	fc.Advance(5 * time.Second)
	requireCircuitOpen(t, d.Handle(context.Background(), newTestEntry(t, []byte(`{}`))))

	// Advancing past OpenTimeout → half-open; probe is admitted, reaches server.
	fc.Advance(customSettings.OpenTimeout + time.Second)
	probeRes := d.Handle(context.Background(), newTestEntry(t, []byte(`{}`)))
	assert.Equal(t, outbox.DispositionRequeue, probeRes.Disposition,
		"half-open probe must reach the endpoint (still 5xx → Requeue, not circuit-open fast-fail)")
}

// TestWithCircuitBreakerSettings_DefaultsUnchangedWhenOptionAbsent verifies the
// no-option path: a Dispatcher without WithCircuitBreakerSettings trips at the
// original default TripThreshold (5) and not earlier.
func TestWithCircuitBreakerSettings_DefaultsUnchangedWhenOptionAbsent(t *testing.T) {
	srv, hits, _ := countingServer(t, http.StatusInternalServerError)

	d, err := NewDispatcher(
		clockmock.New(time.Unix(dispatchTestTS, 0)),
		dispatchTestSigner(t),
		NewSafePolicy(WithAllowLoopback()),
		staticSelector(srv.URL),
		// No WithCircuitBreakerSettings — defaults apply.
	)
	require.NoError(t, err)

	defaults := defaultCircuitBreakerSettings()
	// Deliver exactly TripThreshold times — circuit must still be CLOSED.
	for i := range defaults.TripThreshold {
		res := d.Handle(context.Background(), newTestEntry(t, []byte(`{}`)))
		assert.Equal(t, outbox.DispositionRequeue, res.Disposition,
			"delivery %d: circuit must be closed (default threshold not yet exceeded)", i+1)
	}
	assert.Equal(t, int32(defaults.TripThreshold), hits.Load(), //nolint:gosec // TripThreshold is validated > 0 and << MaxInt32
		"all TripThreshold deliveries must have reached the endpoint")

	// One more exceeds the threshold → trips the circuit.
	d.Handle(context.Background(), newTestEntry(t, []byte(`{}`)))
	requireCircuitOpen(t, d.Handle(context.Background(), newTestEntry(t, []byte(`{}`))))
}
