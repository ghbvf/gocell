package postgres

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/kernel/lifecycle"
	kworker "github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// defaultPGProbeTimeout bounds the inner /readyz probe so a slow PG never
// holds the readyz response indefinitely. See Checkers() comment.
const defaultPGProbeTimeout = 5 * time.Second

// poolCloser is the narrow interface PGResource needs from the pool for
// shutdown. Using an interface instead of *Pool makes Close(ctx) testable via
// a stub without a real database connection.
type poolCloser interface {
	Close(ctx context.Context) error
}

// PGResource wraps a Pool as a lifecycle.ManagedResource. Bootstrap uses it to:
//   - Register the pool health probe in /readyz under the "postgres_ready" name.
//   - Close the pool during LIFO shutdown.
//
// The outbox relay is registered independently via bootstrap.WithManagedResource
// so that its worker lifecycle is managed separately from the pool.
//
// Construct via NewPGResource; do not create the zero value directly.
//
// ref: uber-go/fx lifecycle.go@master:L124-L310 — resource lifecycle managed
// by Hook registration; GoCell converges this into a single ManagedResource.
type PGResource struct {
	pool          *Pool
	name          string                          // health checker name; default "postgres_ready"
	closeOverride poolCloser                      // non-nil only in tests; replaces pool for Close()
	healthFunc    func(ctx context.Context) error // non-nil only in tests; replaces pool.Health
}

// NewPGResource creates a PGResource. pool must be non-nil; passing nil
// returns ErrValidationFailed because Checkers() and Close() dereference
// pool at runtime — a silent nil would produce a panic during /readyz
// probe or shutdown, both of which are the worst times to discover it.
//
// name is always "postgres_ready".
//
// ref: uber-go/fx internal/lifecycle/lifecycle.go Append — resource
// registration does no nil-substitution; bad inputs surface immediately.
func NewPGResource(pool *Pool) (*PGResource, error) {
	if pool == nil {
		return nil, errcode.New(errcode.ErrValidationFailed,
			"NewPGResource: pool must not be nil (Checkers() and Close() dereference pool)")
	}
	return &PGResource{
		pool: pool,
		name: "postgres_ready",
	}, nil
}

// Checkers returns a single health probe named after r.name that pings the PG
// pool. The probe accepts a ctx from the /readyz handler so that a deliberate
// deadline (e.g. WithReadyzDeadline) propagates into the probe; the probe
// further caps its own wait at 5 s via an inner context.WithTimeout so that a
// slow PG does not hold the /readyz response indefinitely.
//
// ctx is derived from context.Background() by the readyz handler to avoid
// kubelet/LB client-ctx cancellation; this probe further bounds at 5s.
//
// ref: cmd/corebundle/main.go:230-241 (pgHealthCheckerOpts) — same rationale,
// same 5s timeout, now centralized here.
// ref: Kubernetes readyz — external dependencies contribute named checks.
func (r *PGResource) Checkers() map[string]func(context.Context) error {
	healthFn := r.healthFunc
	if healthFn == nil {
		healthFn = r.pool.Health
	}
	return map[string]func(context.Context) error{
		r.name: func(ctx context.Context) error {
			probeCtx, cancel := context.WithTimeout(ctx, defaultPGProbeTimeout)
			defer cancel()
			return healthFn(probeCtx)
		},
	}
}

// Worker returns nil — PGResource wraps only the pool and has no background
// worker. The outbox relay is registered as a separate ManagedResource via
// bootstrap.WithManagedResource so its lifecycle is independently managed.
func (r *PGResource) Worker() kworker.Worker {
	return nil
}

// Close shuts down the pool, bounded by ctx. Delegates to Pool.Close(ctx)
// so the caller's shutdown budget propagates into pool drain.
// Uses the poolCloser interface so tests can inject a stub without a real DB.
//
// ref: uber-go/fx app.go StopTimeout — shared shutdown budget via ctx.
func (r *PGResource) Close(ctx context.Context) error {
	return r.closer().Close(ctx)
}

// closer returns the poolCloser for the pool. Indirection allows tests to
// substitute a stub via closeOverride without changing the production field.
func (r *PGResource) closer() poolCloser {
	if r.closeOverride != nil {
		return r.closeOverride
	}
	return r.pool
}

// Compile-time assertion: PGResource must implement lifecycle.ManagedResource.
var _ lifecycle.ManagedResource = (*PGResource)(nil)
