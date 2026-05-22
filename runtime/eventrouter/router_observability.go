package eventrouter

import "time"

// RuntimeErrorReason is the type-safe closed-set for RecordRuntimeError's
// reason argument. Using a named type prevents callers from passing arbitrary
// strings and documents the full value set at the type level.
type RuntimeErrorReason string

// EventCollector receives EventRouter lifecycle observations. The concrete
// production impl is *runtime/observability/metrics.EventRouterCollector.
// Defined here as an interface so router_test.go can use a spy implementation
// without depending on Prometheus.
//
// ref: ThreeDotsLabs/watermill router metrics middleware — subscription lifecycle
// counters and gauges matching router_messages_processed_total / router_handler_active.
type EventCollector interface {
	IncSubscriptionActive(cellID string)
	DecSubscriptionActive(cellID string)
	RecordSetupError(cellID, topic, reason string)
	ObserveReadyWait(cellID string, d time.Duration)
	// RecordRuntimeError records a runtime-phase fault for the given cell+topic.
	// reason is a closed-set value drawn from RuntimeErrorReason* constants;
	// the typed parameter prevents callers from drifting to free-form strings.
	// Phase 4 owner — any failure detected after Running() closes is recorded
	// here as runtime_fault. Phase 1–3 failures live on the setup metric via
	// RecordSetupError (PR #593 review fix-up P2#3).
	RecordRuntimeError(cellID, topic string, reason RuntimeErrorReason)
}

// SetupErrorReason* are the closed-set values passed to
// EventCollector.RecordSetupError. The ADR for the metrics-gaugevec-funnel
// (docs/architecture/202605191500-adr-metrics-gaugevec-funnel.md) enumerates
// all three reasons; adding a new reason requires updating that ADR and every
// collector implementation.
const (
	// SetupErrorReasonSetupError is emitted when Subscriber.Setup returns an
	// error (Phase 1 failure).
	SetupErrorReasonSetupError = "setup_error"

	// SetupErrorReasonReadyTimeout is emitted when one or more subscriptions
	// fail to signal Ready within the configured readyTimeout (Phase 3).
	SetupErrorReasonReadyTimeout = "ready_timeout"

	// SetupErrorReasonPanic is emitted when a subscription goroutine panics
	// inside runSubscribe (Phase 2 unrecoverable failure).
	SetupErrorReasonPanic = "panic"

	// SetupErrorReasonSubscribeFailure is emitted when a subscription
	// goroutine's SubscribeEntry returns an error before the router reaches
	// Running() — i.e. the failure surfaces during Phase 3 await-ready, so
	// it is a setup-phase fault, not a runtime fault.
	//
	// Wave 2 (PR #593 review fix-up): introduced to anchor the
	// phase-classified reason at the Phase 3 consumer point of setupErr,
	// removing the producer-side `select <-r.running` flicker that mixed
	// this failure with RuntimeErrorReasonRuntimeFault.
	SetupErrorReasonSubscribeFailure = "subscribe_failure"
)

// RuntimeErrorReason* are the closed-set values passed to
// EventCollector.RecordRuntimeError. Runtime errors occur after startup
// (Phase 4) and are semantically distinct from setup errors (Phase 1-3).
//
// PR #593 review fix-up (P1#1 + P2#3): the reason set is intentionally
// single-valued. Phase 3 setup-phase failures (SubscribeEntry returning an
// error before Running(), or ready timeout) are owned by the setup metric
// only; the previous RuntimeErrorReasonSubscribeFailure / ReadyWaitTimeout
// reasons were a phase-boundary violation that produced double-counting and
// reason flicker. The Phase 4 consumer always records runtime_fault — the
// router is running, so the underlying cause is by definition a runtime
// fault.
const (
	// RuntimeErrorReasonRuntimeFault is emitted for any runtime fault
	// detected in Phase 4 (after Running() closes).
	RuntimeErrorReasonRuntimeFault RuntimeErrorReason = "runtime_fault"
)

// NopEventCollector is the default when no collector is wired. All methods are
// no-ops so production code paths are always safe to call without a nil check.
type NopEventCollector struct{}

// IncSubscriptionActive is a no-op for NopEventCollector (default when no metrics backend is wired).
func (NopEventCollector) IncSubscriptionActive(string) {
	// intentional no-op: null-object pattern (Sonar S1186 — explicit body comment).
}

// DecSubscriptionActive is a no-op for NopEventCollector (default when no metrics backend is wired).
func (NopEventCollector) DecSubscriptionActive(string) {
	// intentional no-op: null-object pattern (Sonar S1186 — explicit body comment).
}

// RecordSetupError is a no-op for NopEventCollector (default when no metrics backend is wired).
func (NopEventCollector) RecordSetupError(string, string, string) {
	// intentional no-op: null-object pattern (Sonar S1186 — explicit body comment).
}

// ObserveReadyWait is a no-op for NopEventCollector (default when no metrics backend is wired).
func (NopEventCollector) ObserveReadyWait(string, time.Duration) {
	// intentional no-op: null-object pattern (Sonar S1186 — explicit body comment).
}

// RecordRuntimeError is a no-op for NopEventCollector (default when no metrics backend is wired).
func (NopEventCollector) RecordRuntimeError(string, string, RuntimeErrorReason) {
	// intentional no-op: null-object pattern (Sonar S1186 — explicit body comment).
}
