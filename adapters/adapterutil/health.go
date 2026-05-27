package adapterutil

import (
	"context"
	"time"

	"github.com/ghbvf/gocell/kernel/healthz"
)

// DefaultProbeTimeout bounds a /readyz probe so a slow dependency does not
// hold the readyz response indefinitely. 5s matches Kubernetes /readyz
// conventions and the prior PGResource probe timeout (now centralized).
const DefaultProbeTimeout = 5 * time.Second

// HealthToProbe wraps a Health(ctx) error function as a single typed
// healthz.Probe. The probe applies an inner context.WithTimeout(timeout) so a
// slow dependency does not hold /readyz indefinitely. When timeout <= 0 the
// helper substitutes DefaultProbeTimeout.
//
// This centralizes the "Health → Probe + inner deadline" boilerplate that
// would otherwise be duplicated across every adapter implementing
// lifecycle.ManagedResource. It is the dual to CloseWithDeadline for the
// readiness-probe side of the contract.
//
// For adapters requiring multiple named probes (e.g. postgres.Pool exposes
// postgres_ready + postgres_indexes_valid_ready), build the []Probe slice
// directly in the adapter; this helper is intentionally scoped to the common
// single-probe case. A multi-entry variant was considered but deferred until
// a third multi-probe caller emerges (YAGNI).
//
// The name argument is a healthz.ProbeName-typed const declared at the
// caller's adapter (e.g. redis.ProbeReady); a bare string literal here fails
// archtest PROBENAME-SEALED-FUNNEL-01. The probe is constructed via
// healthz.NewProbe so the typed-name discipline persists into the registry.
//
// ref: kubernetes/kubernetes pkg/util/healthz — named health checkers with
// per-probe deadlines.
// ref: uber-go/fx app.go StopTimeout — same shared-deadline pattern, dual side.
func HealthToProbe(
	name healthz.ProbeName,
	healthFn func(context.Context) error,
	timeout time.Duration,
) healthz.Probe {
	if timeout <= 0 {
		timeout = DefaultProbeTimeout
	}
	return healthz.NewProbe(name, func(ctx context.Context) error {
		probeCtx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return healthFn(probeCtx)
	})
}
