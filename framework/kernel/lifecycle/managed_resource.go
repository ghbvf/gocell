package lifecycle

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/healthz"
	"github.com/ghbvf/gocell/framework/kernel/worker"
)

// ManagedResource collects the lifecycle concerns of an external resource
// (pool, relay, RMQ connection) into a single interface. Bootstrap unpacks
// the three aspects — typed health probes, background workers, and LIFO
// teardown — so callers only implement one interface per resource instead
// of three separate bootstrap options.
//
// ref: uber-go/fx internal/lifecycle/lifecycle.go@master:L124-L310 —
// Hook OnStart/OnStop registered in declaration order, stopped in LIFO order.
// ref: go-kratos/kratos transport/transport.go@main:L14-L17 —
// Server interface as a resource-management contract.
type ManagedResource interface {
	// Probes returns the typed readiness probes contributed by this resource.
	// Each probe carries a healthz.ProbeName (typed const, sanctioned by
	// archtest PROBENAME-SEALED-FUNNEL-01 — no bare strings) and a
	// context-aware Check function (nil return = healthy, non-nil = degraded
	// or down). The context carries the /readyz deadline so probes honor
	// cancellation. An empty/nil slice is valid (no probes contributed).
	//
	// Probes() supersedes the legacy Checkers() map[string]func — the typed
	// slice form closes the bare-string ingress that the composition-root
	// NewProbeName conversion previously had to absorb, so adapter ProbeName
	// consts are now the single authoritative source for every probe name
	// reaching /readyz.
	Probes() []healthz.Probe

	// Worker returns the optional background worker for this resource.
	// Returning nil means no background goroutine is needed; bootstrap skips
	// WithWorkers registration for this resource.
	Worker() worker.Worker

	// Close releases the resource, bounded by ctx. Called in LIFO order relative
	// to registration during shutdown. ctx carries the shared phase10 shutdown
	// budget; implementations SHOULD honor ctx.Done for drain operations.
	// Errors are logged as slog.Warn but do not abort the shutdown of other
	// resources (best-effort).
	//
	// ref: ContextCloser — same ctx-aware Close semantics; ManagedResource
	// bundles probes + worker alongside.
	Close(ctx context.Context) error
}
