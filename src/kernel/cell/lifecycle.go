package cell

import "context"

// ---------------------------------------------------------------------------
// Optional lifecycle hook interfaces
// ---------------------------------------------------------------------------
// These interfaces are optionally implemented by Cells to run logic around
// the core Start/Stop lifecycle. During assembly startup and shutdown, the
// orchestrator discovers them via type assertion:
//
//	if bs, ok := c.(BeforeStarter); ok {
//	    if err := bs.BeforeStart(ctx); err != nil {
//	        return err
//	    }
//	}
//
// This keeps the core Cell interface unchanged while allowing Cells to
// opt-in to lifecycle hooks.
//
// ref: go-kratos/kratos app.go — BeforeStart/AfterStart/BeforeStop/AfterStop
// ref: uber-go/fx lifecycle.go — FIFO Start / LIFO Stop / rollback on failure

// BeforeStarter is optionally implemented by Cells that need to run logic
// before Start is called (e.g., validate runtime prerequisites, warm caches,
// acquire external resources). If BeforeStart returns an error, Start is NOT
// called and the assembly rolls back previously-started cells.
type BeforeStarter interface {
	BeforeStart(ctx context.Context) error
}

// AfterStarter is optionally implemented by Cells that need to run logic
// after Start completes (e.g., register health probes, announce readiness).
// If AfterStart returns an error, the cell (whose Start already succeeded)
// is stopped, then previously-started cells are rolled back.
type AfterStarter interface {
	AfterStart(ctx context.Context) error
}

// BeforeStopper is optionally implemented by Cells that need to run logic
// before Stop is called (e.g., drain in-flight requests, deregister from
// service discovery). Errors are accumulated but do not prevent Stop from
// being called.
type BeforeStopper interface {
	BeforeStop(ctx context.Context) error
}

// AfterStopper is optionally implemented by Cells that need to run logic
// after Stop completes (e.g., emit final metrics, close audit logs).
// Errors are accumulated but do not prevent other cells from being stopped.
type AfterStopper interface {
	AfterStop(ctx context.Context) error
}
