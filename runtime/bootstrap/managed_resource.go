package bootstrap

import (
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/runtime/worker"
)

// ManagedResource collects the lifecycle concerns of an external resource
// (pool, relay, RMQ connection) into a single interface. Bootstrap unpacks
// the three aspects — health checking, background workers, and LIFO teardown —
// so callers only implement one interface per resource instead of three
// separate bootstrap options.
//
// ref: uber-go/fx internal/lifecycle/lifecycle.go@master:L124-L310 —
// Hook OnStart/OnStop registered in declaration order, stopped in LIFO order.
// ref: go-kratos/kratos transport/transport.go@main:L14-L17 —
// Server interface as a resource-management contract.
type ManagedResource interface {
	// Checkers returns named health probe functions that contribute to /readyz.
	// Each key is a unique checker name; each value is a func() error probe
	// (nil return = healthy, non-nil = unhealthy). An empty map is valid.
	Checkers() map[string]func() error

	// Worker returns the optional background worker for this resource.
	// Returning nil means no background goroutine is needed; bootstrap skips
	// WithWorkers registration for this resource.
	Worker() worker.Worker

	// Close releases the resource. Called in LIFO order relative to registration
	// during shutdown. Errors are logged as slog.Warn but do not abort the
	// shutdown of other resources (best-effort).
	Close() error
}

// WithManagedResource registers an external resource with the bootstrap
// lifecycle. At Run() time, bootstrap:
//
//  1. Registers each Checkers() entry as a named /readyz health probe.
//  2. Registers the Worker() (when non-nil) with the bootstrap WorkerGroup.
//  3. Appends a LIFO teardown that calls Close() during shutdown.
//
// Multiple calls to WithManagedResource are supported; Close() order is LIFO
// (last registered is first closed), mirroring fx hook order.
//
// ref: uber-go/fx app.go — Option pattern; each Option targets a single concern.
func WithManagedResource(r ManagedResource) Option {
	return func(b *Bootstrap) {
		b.managedResources = append(b.managedResources, r)
	}
}

// expandManagedResources converts the registered ManagedResources into concrete
// bootstrap fields: health checkers, workers, and LIFO teardown closures.
// It is called at the beginning of Run() before any startup step so that
// health checker validation (Step 0) covers resource-contributed checkers too.
//
// LIFO teardown: resources are appended to b.managedResourceTeardowns in
// registration order; Run() iterates teardowns in reverse to achieve LIFO.
func (b *Bootstrap) expandManagedResources() {
	for _, r := range b.managedResources {
		// Expand health checkers.
		for name, fn := range r.Checkers() {
			b.healthCheckers = append(b.healthCheckers, namedChecker{name: name, fn: fn})
		}
		// Expand worker (skip nil).
		if w := r.Worker(); w != nil {
			b.workers = append(b.workers, w)
		}
		// Register LIFO teardown. Capture r in a local so the closure is
		// bound to this iteration's resource, not the loop variable.
		res := r
		b.managedResourceTeardowns = append(b.managedResourceTeardowns, func() {
			if err := res.Close(); err != nil {
				slog.Warn("managed resource Close failed",
					slog.String("resource_type", fmt.Sprintf("%T", res)),
					slog.Any("error", err))
			}
		})
	}
}
