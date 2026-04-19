package postgres

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/ghbvf/gocell/runtime/worker"
)

// poolCloser is the narrow interface PGResource needs from the pool for
// shutdown. Using an interface instead of *Pool makes Close() testable via
// a stub without a real database connection.
type poolCloser interface {
	Close()
}

// PGResource wraps a Pool (and an optional relay worker) as a
// bootstrap.ManagedResource. Bootstrap uses it to:
//   - Register the pool health probe in /readyz under the "postgres" name.
//   - Start/stop the relay worker through the bootstrap WorkerGroup.
//   - Close the pool during LIFO shutdown.
//
// Construct via NewPGResource; do not create the zero value directly.
//
// ref: uber-go/fx lifecycle.go@master:L124-L310 — resource lifecycle managed
// by Hook registration; GoCell converges this into a single ManagedResource.
type PGResource struct {
	pool          *Pool
	relay         worker.Worker                   // optional; nil = no relay worker
	name          string                          // health checker name; default "postgres"
	closeOverride poolCloser                      // non-nil only in tests; replaces pool for Close()
	healthFunc    func(ctx context.Context) error // non-nil only in tests; replaces pool.Health
}

// NewPGResource creates a PGResource. relay may be nil when no relay worker is
// needed (e.g. in-memory outbox mode). name defaults to "postgres" when empty.
func NewPGResource(pool *Pool, relay worker.Worker) *PGResource {
	return &PGResource{
		pool:  pool,
		relay: relay,
		name:  "postgres",
	}
}

// Checkers returns a single health probe named after r.name that pings the PG
// pool. Each probe call creates a fresh 5-second context from context.Background()
// so that a SIGTERM cancelling the parent context does not cause the probe to
// fail immediately — K8s cannot distinguish "PG down" from "process shutting
// down" if the outer ctx is passed directly.
//
// ref: cmd/core-bundle/main.go:230-241 (pgHealthCheckerOpts) — same rationale,
// same 5s timeout, now centralised here.
// ref: Kubernetes readyz — external dependencies contribute named checks.
func (r *PGResource) Checkers() map[string]func() error {
	healthFn := r.healthFunc
	if healthFn == nil {
		healthFn = r.pool.Health
	}
	return map[string]func() error{
		r.name: func() error {
			probeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			return healthFn(probeCtx)
		},
	}
}

// Worker returns the relay worker (may be nil).
func (r *PGResource) Worker() worker.Worker {
	return r.relay
}

// Close shuts down the pool. Always returns nil; pool.Close() is void.
// Uses the poolCloser interface so tests can inject a stub without a real DB.
func (r *PGResource) Close() error {
	r.closer().Close()
	return nil
}

// closer returns the poolCloser for the pool. Indirection allows tests to
// substitute a stub via closeOverride without changing the production field.
func (r *PGResource) closer() poolCloser {
	if r.closeOverride != nil {
		return r.closeOverride
	}
	return r.pool
}

// Compile-time assertion: PGResource must implement bootstrap.ManagedResource.
var _ bootstrap.ManagedResource = (*PGResource)(nil)
