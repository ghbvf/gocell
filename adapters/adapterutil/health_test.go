package adapterutil_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ghbvf/gocell/adapters/adapterutil"
	"github.com/ghbvf/gocell/framework/kernel/healthz"
	"github.com/ghbvf/gocell/framework/pkg/testutil/testtime"
)

var fooReady = healthz.MustProbeName("foo_ready")

func TestHealthToProbe_ReturnsSingleNamedProbe(t *testing.T) {
	t.Parallel()

	p := adapterutil.HealthToProbe(fooReady, func(context.Context) error {
		return nil
	}, testtime.D1s)
	if p == nil {
		t.Fatal("want non-nil probe")
	}
	if got := p.Name(); got != fooReady {
		t.Fatalf("want name %q, got %q", fooReady, got)
	}
}

func TestHealthToProbe_HealthyDelegatesNil(t *testing.T) {
	t.Parallel()

	p := adapterutil.HealthToProbe(fooReady, func(context.Context) error {
		return nil
	}, testtime.D1s)
	if err := p.Check(context.Background()); err != nil {
		t.Fatalf("want nil, got %v", err)
	}
}

func TestHealthToProbe_ErrorPropagated(t *testing.T) {
	t.Parallel()

	sentinel := errors.New("boom")
	p := adapterutil.HealthToProbe(fooReady, func(context.Context) error {
		return sentinel
	}, testtime.D1s)
	if err := p.Check(context.Background()); !errors.Is(err, sentinel) {
		t.Fatalf("want %v, got %v", sentinel, err)
	}
}

func TestHealthToProbe_InnerTimeoutBoundsProbe(t *testing.T) {
	t.Parallel()

	p := adapterutil.HealthToProbe(fooReady, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}, testtime.D50ms)
	start := time.Now()
	err := p.Check(context.Background())
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
	if elapsed > testtime.D200ms {
		t.Errorf("inner timeout not honored: elapsed=%v", elapsed)
	}
}

func TestHealthToProbe_DefaultTimeoutWhenZero(t *testing.T) {
	t.Parallel()

	// timeout=0 substitutes DefaultProbeTimeout; verify by observing that
	// a fast-returning healthFn under timeout=0 still succeeds (probe is not
	// rejected for zero deadline) and that DefaultProbeTimeout is exported.
	if adapterutil.DefaultProbeTimeout != testtime.D5s {
		t.Fatalf("DefaultProbeTimeout = %v, want %v", adapterutil.DefaultProbeTimeout, testtime.D5s)
	}
	p := adapterutil.HealthToProbe(fooReady, func(context.Context) error {
		return nil
	}, 0)
	if err := p.Check(context.Background()); err != nil {
		t.Fatalf("want nil with default timeout, got %v", err)
	}
}

func TestHealthToProbe_InheritsCallerDeadline(t *testing.T) {
	t.Parallel()

	// When the caller passes a tighter ctx, the inner deadline must not
	// extend it — the probe still respects ctx cancellation.
	ctx, cancel := context.WithTimeout(context.Background(), testtime.D20ms)
	defer cancel()
	p := adapterutil.HealthToProbe(fooReady, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	}, testtime.D5s)
	start := time.Now()
	err := p.Check(ctx)
	elapsed := time.Since(start)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
	if elapsed > testtime.D200ms {
		t.Errorf("caller deadline not honored: elapsed=%v", elapsed)
	}
}
