package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	kernellifecycle "github.com/ghbvf/gocell/kernel/lifecycle"
)

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
// A nil r is rejected at phase0 with a fatal error so operators are not
// silently left without the intended resource integration; mirrors the
// WithCircuitBreaker / WithRelayHealth / WithBrokerHealth fail-fast pattern.
//
// ref: uber-go/fx app.go — Option pattern; each Option targets a single concern.
// ref: uber-go/fx internal/lifecycle/lifecycle.go Append — hook registration
// does no nil-substitution; bad inputs surface before any component starts.
//
// Typed-nil contract: WithManagedResource checks `r == nil` (bare interface nil)
// but does not reflect-check wrapped typed-nil pointers such as
// `WithManagedResource((*adapterpg.PGResource)(nil))`. This is intentional —
// construction is the responsibility of resource-specific constructors
// (e.g. adapterpg.NewPGResource rejects pool==nil at construction), so a
// typed-nil wrapper reaching Run() represents a wiring bug upstream, not
// a case Bootstrap should defend against.
func WithManagedResource(r kernellifecycle.ManagedResource) Option {
	return func(b *Bootstrap) {
		if r == nil {
			b.managedResourceNil = true
			return
		}
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
		b.managedResourceTeardowns = append(b.managedResourceTeardowns, func(ctx context.Context) {
			if err := res.Close(ctx); err != nil {
				slog.Warn("managed resource Close failed",
					slog.String("resource_type", fmt.Sprintf("%T", res)),
					slog.Any("error", err))
			}
		})
	}
}
