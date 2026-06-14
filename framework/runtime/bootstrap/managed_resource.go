package bootstrap

import (
	"context"
	"fmt"
	"log/slog"

	kernellifecycle "github.com/ghbvf/gocell/framework/kernel/lifecycle"
	"github.com/ghbvf/gocell/framework/pkg/validation"
)

// WithManagedResource registers an external resource with the bootstrap
// lifecycle. At Run() time, bootstrap:
//
//  1. Registers each Probes() entry as a typed /readyz health probe.
//  2. Registers the Worker() (when non-nil) with the bootstrap WorkerGroup.
//  3. Appends a LIFO teardown that calls Close() during shutdown.
//
// Multiple calls to WithManagedResource are supported; Close() order is LIFO
// (last registered is first closed), mirroring fx hook order.
//
// Both bare-nil and typed-nil (non-nil interface holding a nil pointer) are
// rejected at phase0 with a fatal error, mirroring the WithCircuitBreaker
// fail-fast pattern. This prevents a silent wiring bug from panicking at
// Checkers()/Worker()/Close() call time.
//
// ref: uber-go/fx app.go — Option pattern; each Option targets a single concern.
// ref: uber-go/fx internal/lifecycle/lifecycle.go Append — hook registration
// does no nil-substitution; bad inputs surface before any component starts.
func WithManagedResource(r kernellifecycle.ManagedResource) Option {
	return func(b *Bootstrap) {
		if validation.IsNilInterface(r) {
			b.managedResourceNil = true
			return
		}
		b.managedResources = append(b.managedResources, r)
	}
}

// expandManagedResources converts the registered ManagedResources into concrete
// bootstrap fields: typed health probes, workers, and LIFO teardown closures.
// It is called at the beginning of Run() before any startup step so that
// health checker validation (Step 0) covers resource-contributed probes too.
//
// Returns an error if two resources register the same probe name (duplicate
// probe fail-fast): silently shadowing a probe would cause health
// misreporting that is very difficult to debug at runtime.
//
// Since ManagedResource.Probes() returns typed healthz.Probe values (each
// carrying a sanctioned healthz.ProbeName), there is no bare-string ingress
// here — every probe name reaching /readyz originates as a typed const at
// the adapter/runtime declaration site (PROBENAME-SEALED-FUNNEL-01 funnel).
//
// LIFO teardown: resources are appended to b.managedResourceTeardowns in
// registration order; Run() iterates teardowns in reverse to achieve LIFO.
func (b *Bootstrap) expandManagedResources() error {
	seen := make(map[string]struct{})
	for _, r := range b.managedResources {
		for _, probe := range r.Probes() {
			name := probe.Name()
			key := string(name)
			if _, exists := seen[key]; exists {
				return fmt.Errorf("bootstrap: duplicate probe name %q from ManagedResource %T — "+
					"each managed resource must expose unique probe names", key, r)
			}
			seen[key] = struct{}{}
			p := probe // capture
			b.healthCheckers = append(b.healthCheckers, namedChecker{name: name, fn: p.Check})
		}
		// Expand worker (skip nil).
		if w := r.Worker(); w != nil {
			b.workers = append(b.workers, w)
		}
		// Register LIFO teardown. Capture r in a local so the closure is
		// bound to this iteration's resource, not the loop variable.
		res := r
		resourceType := fmt.Sprintf("%T", res)
		b.managedResourceTeardowns = append(b.managedResourceTeardowns, namedTeardown{
			name: resourceType,
			fn: func(ctx context.Context) error {
				err := res.Close(ctx)
				if err != nil {
					slog.Warn("managed resource Close failed",
						slog.String("resource_type", resourceType),
						slog.Any("error", err))
				}
				return err
			},
		})
	}
	return nil
}
