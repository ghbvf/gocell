package adapterutil_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/adapters/adapterutil"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

func TestHealthToCheckers_ReturnsSingleNamedProbe(t *testing.T) {
	t.Parallel()

	probes := adapterutil.HealthToCheckers("foo_ready", func(context.Context) error {
		return nil
	}, testtime.D1s)
	if len(probes) != 1 {
		t.Fatalf("want 1 probe, got %d", len(probes))
	}
	if _, ok := probes["foo_ready"]; !ok {
		t.Fatalf("want key foo_ready, got %v", probes)
	}
}

func TestHealthToCheckers_HealthyDelegatesNil(t *testing.T) {
	t.Parallel()

	probes := adapterutil.HealthToCheckers("foo_ready", func(context.Context) error {
		return nil
	}, testtime.D1s)
	if err := probes["foo_ready"](context.Background()); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestHealthToCheckers_ErrorPropagated(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("boom")
	probes := adapterutil.HealthToCheckers("foo_ready", func(context.Context) error {
		return sentinel
	}, testtime.D1s)
	if err := probes["foo_ready"](context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("want %v, got %v", sentinel, err)
	}
}

func TestHealthToCheckers_InnerTimeoutBoundsProbe(t *testing.T) {
	t.Parallel()

	probes := adapterutil.HealthToCheckers("foo_ready", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}, testtime.D50ms)
	start := time.Now()
	err := probes["foo_ready"](context.Background())
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
	if elapsed > testtime.D200ms {
		t.Errorf("inner timeout not honored: elapsed=%v", elapsed)
	}
}

func TestHealthToCheckers_DefaultTimeoutWhenZero(t *testing.T) {
	t.Parallel()

	// timeout=0 substitutes DefaultProbeTimeout; verify by observing that
	// a fast-returning healthFn under timeout=0 still succeeds (probe is not
	// rejected for zero deadline) and that DefaultProbeTimeout is exported.
	if adapterutil.DefaultProbeTimeout != testtime.D5s {
		t.Fatalf("DefaultProbeTimeout = %v, want %v", adapterutil.DefaultProbeTimeout, testtime.D5s)
	}
	probes := adapterutil.HealthToCheckers("foo_ready", func(context.Context) error {
		return nil
	}, 0)
	if err := probes["foo_ready"](context.Background()); err != nil {
		t.Fatalf("want nil with default timeout, got %v", err)
	}
}

func TestHealthToCheckers_InheritsCallerDeadline(t *testing.T) {
	t.Parallel()

	// When the caller passes a tighter ctx, the inner deadline must not
	// extend it — the probe still respects ctx cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D20ms)
	defer cancel()
	probes := adapterutil.HealthToCheckers("foo_ready", func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}, testtime.D5s)
	start := time.Now()
	err := probes["foo_ready"](ctx)
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
	if elapsed > testtime.D200ms {
		t.Errorf("caller deadline not honored: elapsed=%v", elapsed)
	}
}
