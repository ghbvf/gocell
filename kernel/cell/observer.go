package cell

import "time"

// HookPhase identifies a specific lifecycle hook invocation site.
//
// ref: uber-go/fx fxevent/event.go@master — OnStart / OnStop events are
// emitted around the corresponding hook calls; GoCell splits the same
// hooks into four distinct phases to match the existing Before/After
// interfaces in lifecycle.go.
type HookPhase string

const (
	HookBeforeStart HookPhase = "before_start"
	HookAfterStart  HookPhase = "after_start"
	HookBeforeStop  HookPhase = "before_stop"
	HookAfterStop   HookPhase = "after_stop"
)

// HookOutcome classifies the result of a hook invocation.
//
// Outcome is derived by the assembly layer from the hook's return value:
//   - nil error                          → OutcomeSuccess
//   - context.DeadlineExceeded wrapped   → OutcomeTimeout
//   - panic recovered by callHookSafe    → OutcomePanic
//   - any other non-nil error            → OutcomeFailure
type HookOutcome string

const (
	OutcomeSuccess HookOutcome = "success"
	OutcomeFailure HookOutcome = "failure"
	OutcomeTimeout HookOutcome = "timeout"
	OutcomePanic   HookOutcome = "panic"
)

// HookEvent is emitted to the LifecycleHookObserver once per hook
// invocation (after the hook completes or times out).
//
// ref: uber-go/fx fxevent/event.go@master:L82-L89 OnStartExecuted — carries
// Runtime + Err. GoCell adds CellID (hooks are per-cell in the assembly
// loop) and splits Hook into four phases matching the lifecycle.go
// interfaces, so event type is redundant.
type HookEvent struct {
	CellID   string
	Hook     HookPhase
	Outcome  HookOutcome
	Duration time.Duration
	Err      error
}

// LifecycleHookObserver observes lifecycle hook invocations for a running
// assembly. The assembly calls OnHookEvent exactly once per hook, after
// the hook returns (successfully, with error, timeout, or panic).
//
// Implementations MUST be safe for concurrent use — during rollback the
// assembly may emit stop-phase events from multiple cells serially; there
// is no parallel call today, but implementations should not rely on
// serialization. Implementations MUST NOT block the caller (use an async
// fan-out internally if the sink is slow).
//
// Implementations MUST NOT panic. The assembly wraps calls in a panic
// guard as defense in depth, but a panicking observer signals a bug.
//
// kernel/cell has zero Prometheus dependency by design — concrete
// implementations live in runtime/observability or adapters/.
//
// ref: uber-go/fx fxevent/logger.go@master:L24-L27 — single-method Logger
// interface; NopLogger as null object. GoCell mirrors this shape.
type LifecycleHookObserver interface {
	OnHookEvent(HookEvent)
}

// NopHookObserver is the zero-value observer used when none is configured.
// Its OnHookEvent is a no-op; it exists to avoid nil checks on every
// hook emission.
//
// ref: uber-go/fx fxevent/logger.go@master:L29-L37 NopLogger — same pattern.
type NopHookObserver struct{}

// OnHookEvent is a no-op: the default observer discards all events.
// Business reason: LifecycleHookObserver is an optional collaborator
// injected via AssemblyConfig; when unconfigured, the assembly substitutes
// NopHookObserver to keep the call site unconditional and allocation-free.
func (NopHookObserver) OnHookEvent(HookEvent) {}
