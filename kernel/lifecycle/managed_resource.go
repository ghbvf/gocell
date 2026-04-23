package lifecycle

import (
	"context"

	"github.com/ghbvf/gocell/kernel/worker"
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
	// Each key is a unique checker name; each value is a context-aware probe
	// (nil return = healthy, non-nil = unhealthy). The context carries the
	// /readyz deadline so probes can honour cancellation. An empty map is valid.
	Checkers() map[string]func(context.Context) error

	// Worker returns the optional background worker for this resource.
	// Returning nil means no background goroutine is needed; bootstrap skips
	// WithWorkers registration for this resource.
	Worker() worker.Worker

	// Close releases the resource, bounded by ctx. Called in LIFO order relative
	// to registration during shutdown. ctx carries the shared phase10 shutdown
	// budget; implementations SHOULD honour ctx.Done for drain operations.
	// Errors are logged as slog.Warn but do not abort the shutdown of other
	// resources (best-effort).
	//
	// ref: ContextCloser — same ctx-aware Close semantics; ManagedResource
	// bundles health + worker alongside.
	Close(ctx context.Context) error
}
