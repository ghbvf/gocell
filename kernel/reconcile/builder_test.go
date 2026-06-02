package reconcile

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// realTrigger is a minimal no-op Trigger for builder tests — avoids import-cycle
// with reconciletest (reconciletest imports reconcile; builder_test is package reconcile).
type realTrigger struct{}

func (realTrigger) Start(_ context.Context, _ chan<- Request) error { return nil }

// builderExplicitInterval is the explicit interval used by the
// interval_explicit_unchanged case (TEST-TIME-LITERAL-01: package-level const).
const builderExplicitInterval = 7 * time.Second

// ─────────────────────────────────────────────────────────────────────────────
// TestBuilder_RequiresReconcilerAndTrigger: missing reconciler or trigger → error
// ─────────────────────────────────────────────────────────────────────────────

func TestBuilder_RequiresNonNilReconciler(t *testing.T) {
	t.Parallel()
	// untyped nil Reconciler → Build must return error
	_, err := New(nil).WithTrigger(realTrigger{}).Build()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Reconciler")
}

func TestBuilder_TypedNilReconcilerFails(t *testing.T) {
	t.Parallel()
	var rec funcReconciler // zero-value (typed nil func)
	_, err := New(rec).WithTrigger(realTrigger{}).Build()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Reconciler")
}

func TestBuilder_RequiresTrigger(t *testing.T) {
	t.Parallel()
	// No WithTrigger → Build must return error mentioning "Trigger"
	rec := funcReconciler(func(_ context.Context, _ Request) (Result, error) {
		return Result{}, nil
	})
	_, err := New(rec).Build()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "Trigger")
}

func TestBuilder_ValidBuildSucceeds(t *testing.T) {
	t.Parallel()
	rec := funcReconciler(func(_ context.Context, _ Request) (Result, error) {
		return Result{}, nil
	})
	loop, err := New(rec).WithTrigger(realTrigger{}).Build()
	require.NoError(t, err)
	require.NotNil(t, loop)
}

// ─────────────────────────────────────────────────────────────────────────────
// TestBuilder_DefaultsLeaderAsNoop: no WithLeader → loop.leader == nil
// ─────────────────────────────────────────────────────────────────────────────

func TestBuilder_DefaultsLeaderAsNoop(t *testing.T) {
	t.Parallel()
	rec := funcReconciler(func(_ context.Context, _ Request) (Result, error) {
		return Result{}, nil
	})
	loop, err := New(rec).WithTrigger(realTrigger{}).Build()
	require.NoError(t, err)
	assert.Nil(t, loop.leader, "no WithLeader → loop.leader must be nil (single-process mode)")
}

// ─────────────────────────────────────────────────────────────────────────────
// TestBuilder_DefaultsConcurrencyTo1: applyDefaults fills zero maxConcurrent to 1
// ─────────────────────────────────────────────────────────────────────────────

func TestBuilder_DefaultsConcurrencyTo1(t *testing.T) {
	t.Parallel()
	rec := funcReconciler(func(_ context.Context, _ Request) (Result, error) {
		return Result{}, nil
	})
	loop, err := New(rec).WithTrigger(realTrigger{}).Build()
	require.NoError(t, err)
	// Before Start, maxConcurrentReconciles may still be 0 (Build transfers explicit
	// value, applyDefaults fires at Start). Test the defaulting funnel directly.
	loop.applyDefaults()
	assert.Equal(t, defaultMaxConcurrentReconciles, loop.maxConcurrentReconciles)
}

// ─────────────────────────────────────────────────────────────────────────────
// TestLoop_ApplyDefaults: covers each default branch of applyDefaults().
// These assertions absorb the deleted loop_getters_test.go.
// ─────────────────────────────────────────────────────────────────────────────

func TestLoop_ApplyDefaults(t *testing.T) {
	t.Parallel()

	t.Run("interval_zero_becomes_default", func(t *testing.T) {
		t.Parallel()
		l := &Loop{}
		l.applyDefaults()
		assert.Equal(t, defaultReconcileInterval, l.interval)
	})
	t.Run("interval_explicit_unchanged", func(t *testing.T) {
		t.Parallel()
		l := &Loop{interval: builderExplicitInterval}
		l.applyDefaults()
		assert.Equal(t, builderExplicitInterval, l.interval)
	})
	t.Run("maxConcurrent_zero_becomes_1", func(t *testing.T) {
		t.Parallel()
		l := &Loop{}
		l.applyDefaults()
		assert.Equal(t, defaultMaxConcurrentReconciles, l.maxConcurrentReconciles)
	})
	t.Run("maxConcurrent_explicit_unchanged", func(t *testing.T) {
		t.Parallel()
		l := &Loop{maxConcurrentReconciles: 4}
		l.applyDefaults()
		assert.Equal(t, 4, l.maxConcurrentReconciles)
	})
	t.Run("name_empty_becomes_default", func(t *testing.T) {
		t.Parallel()
		l := &Loop{}
		l.applyDefaults()
		assert.Equal(t, defaultLoopName, l.name)
	})
	t.Run("name_explicit_unchanged", func(t *testing.T) {
		t.Parallel()
		l := &Loop{name: "myloop"}
		l.applyDefaults()
		assert.Equal(t, "myloop", l.name)
	})
	t.Run("reconcilerID_empty_becomes_sentinel", func(t *testing.T) {
		t.Parallel()
		l := &Loop{}
		l.applyDefaults()
		assert.Equal(t, reconcilerIDSentinel, l.reconcilerID)
	})
	t.Run("reconcilerID_explicit_unchanged", func(t *testing.T) {
		t.Parallel()
		l := &Loop{reconcilerID: "abc"}
		l.applyDefaults()
		assert.Equal(t, "abc", l.reconcilerID)
	})
	t.Run("logger_nil_becomes_slog_default", func(t *testing.T) {
		t.Parallel()
		l := &Loop{}
		l.applyDefaults()
		assert.NotNil(t, l.logger)
	})
	t.Run("logger_explicit_unchanged", func(t *testing.T) {
		t.Parallel()
		custom := slog.New(slog.NewTextHandler(io.Discard, nil))
		l := &Loop{logger: custom}
		l.applyDefaults()
		assert.Same(t, custom, l.logger)
	})
}

// ─────────────────────────────────────────────────────────────────────────────
// TestBuilder_WithOptionsThreadThrough: each With* sets the corresponding loop field
// ─────────────────────────────────────────────────────────────────────────────

const (
	builderTestInterval      = 5 * time.Second
	builderTestBaseDelay     = 10 * time.Millisecond
	builderTestMaxDelay      = 30 * time.Second
	builderTestStartTimeout  = 2 * time.Second
	builderTestStopTimeout   = 4 * time.Second
	builderTestRenewInterval = 3 * time.Second
)

func TestBuilder_WithOptionsThreadThrough(t *testing.T) {
	t.Parallel()
	rec := funcReconciler(func(_ context.Context, _ Request) (Result, error) {
		return Result{}, nil
	})

	loop, err := New(rec).
		WithTrigger(realTrigger{}).
		WithConcurrency(3).
		WithInterval(builderTestInterval).
		WithBackoff(builderTestBaseDelay, builderTestMaxDelay).
		WithName("myloop").
		WithReconcilerID("myreconciler").
		WithStartTimeout(builderTestStartTimeout).
		WithStopTimeout(builderTestStopTimeout).
		WithRenewInterval(builderTestRenewInterval).
		Build()
	require.NoError(t, err)

	assert.Equal(t, 3, loop.maxConcurrentReconciles)
	assert.Equal(t, builderTestInterval, loop.interval)
	assert.Equal(t, builderTestBaseDelay, loop.baseDelay)
	assert.Equal(t, builderTestMaxDelay, loop.maxDelay)
	assert.Equal(t, "myloop", loop.name)
	assert.Equal(t, "myreconciler", loop.reconcilerID)
	assert.Equal(t, builderTestStartTimeout, loop.startTimeout)
	assert.Equal(t, builderTestStopTimeout, loop.stopTimeout)
	assert.Equal(t, builderTestRenewInterval, loop.renewInterval)
	// reconciler is a func type — compare by nil-ness and type identity, not ==
	assert.NotNil(t, loop.reconciler, "reconciler must be wired into the loop")
}

func TestBuilder_WithMetricsThreadsThrough(t *testing.T) {
	t.Parallel()
	rec := funcReconciler(func(_ context.Context, _ Request) (Result, error) {
		return Result{}, nil
	})
	p := newRecordingProvider()
	m, err := RegisterMetrics(p)
	require.NoError(t, err)

	loop, buildErr := New(rec).WithTrigger(realTrigger{}).WithMetrics(m).Build()
	require.NoError(t, buildErr)
	assert.NotNil(t, loop.metrics.Total)
}

// ─────────────────────────────────────────────────────────────────────────────
// TestBuilder_TriggerWiredIntoSource: trigger channel wired as source
// ─────────────────────────────────────────────────────────────────────────────

func TestBuilder_TriggerWiredIntoSource(t *testing.T) {
	t.Parallel()
	rec := funcReconciler(func(_ context.Context, _ Request) (Result, error) {
		return Result{}, nil
	})
	loop, err := New(rec).WithTrigger(realTrigger{}).Build()
	require.NoError(t, err)
	assert.NotNil(t, loop.source, "loop.source must be wired to the trigger channel")
	assert.NotNil(t, loop.trigger, "loop.trigger must hold the Trigger")
}

// ─────────────────────────────────────────────────────────────────────────────
// renewIntervalFor and currentEpoch — still private, tested directly
// ─────────────────────────────────────────────────────────────────────────────

const (
	applyDefaultsRenewInterval = 3 * time.Second
	applyDefaultsTokenSpan     = 9 * time.Second
)

func TestLoop_RenewIntervalFor_PostPrivatize(t *testing.T) {
	t.Parallel()

	t.Run("explicit_override", func(t *testing.T) {
		t.Parallel()
		l := &Loop{renewInterval: applyDefaultsRenewInterval}
		assert.Equal(t, applyDefaultsRenewInterval, l.renewIntervalFor(LeaseToken{}))
	})
	t.Run("derived_from_token_ttl", func(t *testing.T) {
		t.Parallel()
		now := time.Now()
		tok := LeaseToken{AcquiredAt: now, ExpiresAt: now.Add(applyDefaultsTokenSpan)}
		assert.Equal(t, applyDefaultsTokenSpan/renewIntervalDivisor, (&Loop{}).renewIntervalFor(tok))
	})
	t.Run("zero_ttl_falls_back_to_default", func(t *testing.T) {
		t.Parallel()
		assert.Equal(t, defaultRenewInterval, (&Loop{}).renewIntervalFor(LeaseToken{}))
	})
}

func TestLoop_CurrentEpoch_NoLease_PostPrivatize(t *testing.T) {
	t.Parallel()
	assert.Equal(t, uint64(0), (&Loop{}).currentEpoch())
}
