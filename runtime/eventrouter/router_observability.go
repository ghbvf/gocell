package eventrouter

import "time"

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
	// RecordRuntimeError records a runtime-phase error for the given cell+topic.
	// reason is a closed-set string drawn from RuntimeErrorReason* constants.
	// Wave 2: wired into router.go Phase 4 error paths (subscribe_failure,
	// ready_wait_timeout, runtime_fault).
	RecordRuntimeError(cellID, topic, reason string)
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
)

// RuntimeErrorReason* are the closed-set values passed to
// EventCollector.RecordRuntimeError. Runtime errors occur after startup
// (Phase 4) and are semantically distinct from setup errors (Phase 1-3).
//
// Wave 2: wired into router.go Phase 4 paths.
const (
	// RuntimeErrorReasonSubscribeFailure is emitted when a subscription
	// goroutine's SubscribeEntry returns an error after the router is running.
	RuntimeErrorReasonSubscribeFailure = "subscribe_failure"

	// RuntimeErrorReasonReadyWaitTimeout is emitted when a subscription does
	// not become ready within the ready-wait budget at runtime (Phase 4).
	RuntimeErrorReasonReadyWaitTimeout = "ready_wait_timeout"

	// RuntimeErrorReasonRuntimeFault is emitted for any unclassified runtime
	// fault detected in Phase 4.
	RuntimeErrorReasonRuntimeFault = "runtime_fault"
)

// NopEventCollector is the default when no collector is wired. All methods are
// no-ops so production code paths are always safe to call without a nil check.
type NopEventCollector struct{}

// IncSubscriptionActive is a no-op for NopEventCollector (default when no metrics backend is wired).
func (NopEventCollector) IncSubscriptionActive(string) {}

// DecSubscriptionActive is a no-op for NopEventCollector (default when no metrics backend is wired).
func (NopEventCollector) DecSubscriptionActive(string) {}

// RecordSetupError is a no-op for NopEventCollector (default when no metrics backend is wired).
func (NopEventCollector) RecordSetupError(string, string, string) {}

// ObserveReadyWait is a no-op for NopEventCollector (default when no metrics backend is wired).
func (NopEventCollector) ObserveReadyWait(string, time.Duration) {}

// RecordRuntimeError is a no-op for NopEventCollector (default when no metrics backend is wired).
func (NopEventCollector) RecordRuntimeError(string, string, string) {}
