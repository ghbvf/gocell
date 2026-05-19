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

// NopEventCollector is the default when no collector is wired. All methods are
// no-ops so production code paths are always safe to call without a nil check.
type NopEventCollector struct{}

func (NopEventCollector) IncSubscriptionActive(string)            {}
func (NopEventCollector) DecSubscriptionActive(string)            {}
func (NopEventCollector) RecordSetupError(string, string, string) {}
func (NopEventCollector) ObserveReadyWait(string, time.Duration)  {}
