package bootstrap

// invariants:
//   - INVARIANT: phase0 health-listener fail-fast (#673)
//
// health_listener_required_test.go — phase0 must fail-fast when framework
// health routes (/healthz, /readyz, /metrics) have no dedicated
// cell.HealthListener. The pre-#673 silent remap onto PrimaryListener is gone;
// there is no opt-in escape hatch — a declared listener set without a
// HealthListener is rejected at startup.

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
)

// TestPhase0_NoHealthListener_FailsFast pins the #673 fail-closed invariant:
// declaring listeners without a cell.HealthListener must be rejected at phase0
// rather than silently relocating /healthz + /readyz onto the public primary
// listener.
//
// TDD red-light: the pre-#673 code remaps health groups in phase5 and returns
// no error, so this test FAILS until the phase0 guard lands.
func TestPhase0_NoHealthListener_FailsFast(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		opts []Option
	}{
		{
			name: "primary only",
			opts: []Option{
				WithListener(cell.PrimaryListener, ":8080", []auth.ListenerAuth{auth.AuthNone{}}),
			},
		},
		{
			name: "primary + internal, no health",
			opts: []Option{
				WithListener(cell.PrimaryListener, ":8080", []auth.ListenerAuth{auth.AuthNone{}}),
				WithListener(cell.InternalListener, "127.0.0.1:9090", []auth.ListenerAuth{auth.AuthNone{}}),
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			b := New(append([]Option{WithClock(clock.Real())}, tc.opts...)...)
			err := b.phase0ValidateOptions()

			require.Error(t, err, "phase0 must fail-fast when no HealthListener is declared")
			assert.Contains(t, err.Error(), "HealthListener",
				"error must name cell.HealthListener so operators know the fix")
		})
	}
}

// TestPhase0_MetricsHandler_NoHealthListener_FailsFast covers the former B2
// metrics-specific check. After pure fail-fast it is subsumed by the general
// health-listener requirement — metrics-without-health must still fail.
func TestPhase0_MetricsHandler_NoHealthListener_FailsFast(t *testing.T) {
	t.Parallel()

	b := New(
		WithClock(clock.Real()),
		WithListener(cell.PrimaryListener, ":8080", []auth.ListenerAuth{auth.AuthNone{}}),
		WithHealthRoutes(WithMetricsHandler(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))),
	)
	err := b.phase0ValidateOptions()

	require.Error(t, err, "metrics handler without a HealthListener must fail-fast")
	assert.Contains(t, err.Error(), "HealthListener")
}

// TestPhase0_HealthListenerDeclared_NoHealthError verifies the positive path:
// declaring a HealthListener satisfies the health-routes requirement. phase0
// may still fail for unrelated reasons, so we only assert the health
// requirement is not the cause.
func TestPhase0_HealthListenerDeclared_NoHealthError(t *testing.T) {
	t.Parallel()

	b := New(
		WithClock(clock.Real()),
		WithListener(cell.PrimaryListener, ":8080", []auth.ListenerAuth{auth.AuthNone{}}),
		WithListener(cell.HealthListener, "127.0.0.1:9091", []auth.ListenerAuth{auth.AuthNone{}}),
	)
	err := b.phase0ValidateOptions()
	if err != nil {
		assert.NotContains(t, err.Error(), "HealthListener",
			"declaring a HealthListener must satisfy the health-routes requirement")
	}
}
