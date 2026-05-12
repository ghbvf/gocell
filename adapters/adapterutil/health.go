package adapterutil

import (
	"context"
	"time"
)

// DefaultProbeTimeout bounds a /readyz probe so a slow dependency does not
// hold the readyz response indefinitely. 5s matches Kubernetes /readyz
// conventions and the prior PGResource probe timeout (now centralized).
const DefaultProbeTimeout = 5 * time.Second

// HealthToCheckers wraps a Health(ctx) error function as a single-entry
// lifecycle.ManagedResource.Checkers() map. The probe applies an inner
// context.WithTimeout(timeout) so a slow dependency does not hold /readyz
// indefinitely. When timeout <= 0 the helper substitutes DefaultProbeTimeout.
//
// This centralizes the "Health → Checkers map + inner deadline" boilerplate
// that would otherwise be duplicated across every adapter implementing
// lifecycle.ManagedResource. It is the dual to CloseWithDeadline for the
// readiness-probe side of the contract.
//
// ref: kubernetes/kubernetes pkg/util/healthz — named health checkers with
// per-probe deadlines.
// ref: uber-go/fx app.go StopTimeout — same shared-deadline pattern, dual side.
func HealthToCheckers(name string, healthFn func(context.Context) error, timeout time.Duration) map[string]func(context.Context) error {
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	return map[string]func(context.Context) error{
		name: func(ctx context.Context) error {
			probeCtx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()
			return healthFn(probeCtx)
		},
	}
}
