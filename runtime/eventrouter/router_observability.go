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

// NopEventCollector is the default when no collector is wired. All methods are
// no-ops so production code paths are always safe to call without a nil check.
type NopEventCollector struct{}

func (NopEventCollector) IncSubscriptionActive(string)            {}
func (NopEventCollector) DecSubscriptionActive(string)            {}
func (NopEventCollector) RecordSetupError(string, string, string) {}
func (NopEventCollector) ObserveReadyWait(string, time.Duration)  {}
