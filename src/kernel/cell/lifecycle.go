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
// Compensation boundary: the assembly only performs cleanup (BeforeStop →
// Stop → AfterStop) for cells whose Start has already succeeded. If
// BeforeStart or Start fails, the current cell is NOT cleaned up by the
// framework — only previously-started cells are rolled back. This matches
// Uber fx and Kratos semantics.
//
// ref: go-kratos/kratos app.go — BeforeStart/AfterStart/BeforeStop/AfterStop
// ref: uber-go/fx lifecycle.go — FIFO Start / LIFO Stop / rollback on failure

// BeforeStarter is optionally implemented by Cells that need to run
// preflight checks before Start is called (e.g., validate runtime
// prerequisites, verify config completeness, check external connectivity).
//
// BeforeStart MUST NOT acquire resources that require cleanup. If it
// returns an error, Start is NOT called, and the framework does NOT run
// BeforeStop/Stop/AfterStop on the current cell — only previously-started
// cells are rolled back. Any resource acquisition should happen inside
// Start, which is responsible for its own internal cleanup on failure.
//
// ref: uber-go/fx lifecycle.go — failing OnStart does not trigger OnStop
// for the same hook; only previously-registered hooks are rolled back
type BeforeStarter interface {
	BeforeStart(ctx context.Context) error
}

// AfterStarter is optionally implemented by Cells that need to run logic
// after Start completes (e.g., register health probes, announce readiness).
// If AfterStart returns an error, the cell (whose Start already succeeded)
// is stopped via BeforeStop → Stop → AfterStop, then previously-started
// cells are rolled back.
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
//
// Note: by the time AfterStop runs, the cell's ShutdownCtx() is already
// cancelled (Stop cancels it). Use the ctx parameter passed by the
// assembly, which carries the shutdown timeout, not ShutdownCtx().
type AfterStopper interface {
	AfterStop(ctx context.Context) error
}
