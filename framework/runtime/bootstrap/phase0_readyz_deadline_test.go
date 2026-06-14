package bootstrap

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/healthz"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
	obshealthz "github.com/ghbvf/gocell/framework/runtime/observability/healthz"
)

// TestPhase0_ReadyzDeadline_AppliedToDefaultAggregator is the regression guard
// for F2: WithReadyzDeadline previously routed to a no-op health.WithDeadline
// and never took effect. It is now applied to the default aggregator phase0
// builds, so a probe that outlives the deadline is reported non-Up.
//
// phase0 constructs the default aggregator before listener validation; this
// test only needs that early step, so a later no-listeners error is irrelevant
// to what is asserted.
func TestPhase0_ReadyzDeadline_AppliedToDefaultAggregator(t *testing.T) {
	b := New(clock.Real(), WithReadyzDeadline(testtime.D50ms))
	_ = b.phase0ValidateOptions()
	require.NotNil(t, b.healthAggregator)

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	require.NoError(t, b.healthAggregator.Register(healthz.NewProbe("slow_ready",
		func(ctx context.Context) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-release:
				return nil
			}
		})))

	snap := b.healthAggregator.Evaluate(context.Background())
	require.Len(t, snap.Probes, 1)
	assert.NotEqual(t, healthz.StatusUp, snap.Probes[0].Status,
		"a probe exceeding the readyz deadline must not be reported Up — proves the deadline reached the aggregator")
}

// TestPhase0_ReadyzDeadlineWithCustomAggregator_FailsFast verifies that
// WithReadyzDeadline cannot combine with WithHealthAggregator: a custom
// aggregator owns its own deadline, so the deadline option would be silently
// ineffective. phase0 surfaces the contradictory wiring instead.
func TestPhase0_ReadyzDeadlineWithCustomAggregator_FailsFast(t *testing.T) {
	b := New(
		clock.Real(),
		WithHealthAggregator(obshealthz.NewAggregator(clock.Real())),
		WithReadyzDeadline(testtime.D2s),
	)
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithReadyzDeadline cannot combine with WithHealthAggregator")
}
