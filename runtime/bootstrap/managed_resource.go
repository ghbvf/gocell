package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"

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
		if isNilManagedResource(r) {
			b.managedResourceNil = true
			return
		}
		b.managedResources = append(b.managedResources, r)
	}
}

// isNilManagedResource returns true if r is either a bare nil interface or a
// typed-nil (non-nil interface wrapping a nil pointer/map/slice/chan/func).
// Mirrors the typed-nil rejection used by WithCircuitBreaker.
func isNilManagedResource(r kernellifecycle.ManagedResource) bool {
	if r == nil {
		return true
	}
	v := reflect.ValueOf(r)
	switch v.Kind() {
	case reflect.Pointer, reflect.Map, reflect.Slice, reflect.Chan, reflect.Func, reflect.Interface:
		return v.IsNil()
	default:
		return false
	}
}

// expandManagedResources converts the registered ManagedResources into concrete
// bootstrap fields: health checkers, workers, and LIFO teardown closures.
// It is called at the beginning of Run() before any startup step so that
// health checker validation (Step 0) covers resource-contributed checkers too.
//
// Returns an error if two resources register the same checker key (duplicate
// checker fail-fast): silently shadowing a checker would cause health
// misreporting that is very difficult to debug at runtime.
//
// LIFO teardown: resources are appended to b.managedResourceTeardowns in
// registration order; Run() iterates teardowns in reverse to achieve LIFO.
func (b *Bootstrap) expandManagedResources() error {
	seen := make(map[string]struct{})
	for _, r := range b.managedResources {
		// Expand health checkers: r.Checkers() now returns
		// map[string]func(context.Context) error matching namedChecker.fn type.
		for name, fn := range r.Checkers() {
			if _, exists := seen[name]; exists {
				return fmt.Errorf("bootstrap: duplicate checker key %q from ManagedResource %T — "+
					"each managed resource must expose unique checker names", name, r)
			}
			seen[name] = struct{}{}
			fn := fn // capture
			b.healthCheckers = append(b.healthCheckers, namedChecker{name: name, fn: fn})
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
