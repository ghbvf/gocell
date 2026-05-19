package metrics

import (
	"fmt"
	"time"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// EventRouterCollector registers event-router lifecycle metrics:
//   - event_router_subscriptions_active{cell} (Gauge): active subscription count
//   - event_router_setup_errors_total{cell,topic,reason} (Counter): setup failures
//   - event_router_ready_wait_seconds{cell} (Histogram): time waited for Ready signal
//   - event_router_runtime_errors_total{cell,topic,reason} (Counter): runtime-phase errors
//
// Histogram buckets for ready_wait_seconds are: 0.001, 0.01, 0.1, 0.5, 1, 5, 30 seconds.
// Ready usually completes in <100ms; bootstrap timeout is 30s, so the upper bound
// covers the full expected range.
//
// The reason label for event_router_runtime_errors_total is a closed set:
//   - "subscribe_failure": SubscribeEntry returned an error after startup
//   - "ready_wait_timeout": subscription did not become ready within the ready-wait budget
//   - "runtime_fault": any unclassified runtime fault in Phase 4
//
// ref: Watermill router metrics middleware — subscription lifecycle counters and
// gauges matching the router_messages_processed_total / router_handler_active
// pattern.
type EventRouterCollector struct {
	active     kernelmetrics.GaugeVec     // event_router_subscriptions_active{cell}
	setupErr   kernelmetrics.CounterVec   // event_router_setup_errors_total{cell,topic,reason}
	readyWait  kernelmetrics.HistogramVec // event_router_ready_wait_seconds{cell}
	runtimeErr kernelmetrics.CounterVec   // event_router_runtime_errors_total{cell,topic,reason}
}

// eventRouterReadyWaitBuckets are sensible default buckets for Ready wait time.
// Ready usually completes in <100ms; bootstrap timeout is 30s.
// The 0.25s bucket improves P95 resolution in the 100-500ms range typical for
// broker connection establishment.
var eventRouterReadyWaitBuckets = []float64{0.001, 0.01, 0.1, 0.25, 0.5, 1, 5, 30}

// NewEventRouterCollector registers all three event-router lifecycle metrics on
// the given provider. On partial failure, already-registered metrics are rolled
// back LIFO before returning the error.
func NewEventRouterCollector(p kernelmetrics.Provider) (*EventRouterCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: event router Provider is required")
	}

	active, err := p.GaugeVec(kernelmetrics.GaugeOpts{
		Name:       "event_router_subscriptions_active",
		Help:       "Number of currently active event router subscriptions.",
		LabelNames: []string{"cell"},
	})
	if err != nil {
		return nil, fmt.Errorf("runtime/observability/metrics: register event_router_subscriptions_active: %w", err)
	}

	setupErr, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name:       "event_router_setup_errors_total",
		Help:       "Total number of event router subscription setup failures.",
		LabelNames: []string{"cell", "topic", "reason"},
	})
	if err != nil {
		_ = p.Unregister(active)
		return nil, fmt.Errorf("runtime/observability/metrics: register event_router_setup_errors_total: %w", err)
	}

	readyWait, err := p.HistogramVec(kernelmetrics.HistogramOpts{
		Name:       "event_router_ready_wait_seconds",
		Help:       "Time in seconds waited for event router Ready signal. Ready usually completes in <100ms; bootstrap timeout is 30s.",
		LabelNames: []string{"cell"},
		Buckets:    eventRouterReadyWaitBuckets,
	})
	if err != nil {
		_ = p.Unregister(setupErr)
		_ = p.Unregister(active)
		return nil, fmt.Errorf("runtime/observability/metrics: register event_router_ready_wait_seconds: %w", err)
	}

	runtimeErr, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name:       "event_router_runtime_errors_total",
		Help:       "Total number of event router runtime-phase errors (Phase 4), partitioned by cell, topic, and reason. reason is a closed set: subscribe_failure | ready_wait_timeout | runtime_fault.",
		LabelNames: []string{"cell", "topic", "reason"},
	})
	if err != nil {
		_ = p.Unregister(readyWait)
		_ = p.Unregister(setupErr)
		_ = p.Unregister(active)
		return nil, fmt.Errorf("runtime/observability/metrics: register event_router_runtime_errors_total: %w", err)
	}

	return &EventRouterCollector{
		active:     active,
		setupErr:   setupErr,
		readyWait:  readyWait,
		runtimeErr: runtimeErr,
	}, nil
}

// IncSubscriptionActive increments the active subscription gauge for cellID.
func (c *EventRouterCollector) IncSubscriptionActive(cellID string) {
	if c == nil {
		return
	}
	c.active.With(kernelmetrics.Labels{"cell": cellID}).Inc()
}

// DecSubscriptionActive decrements the active subscription gauge for cellID.
func (c *EventRouterCollector) DecSubscriptionActive(cellID string) {
	if c == nil {
		return
	}
	c.active.With(kernelmetrics.Labels{"cell": cellID}).Dec()
}

// RecordSetupError increments the setup error counter for the given cell, topic
// and reason. The reason argument is drawn from the closed set defined by the
// eventrouter package constants:
//   - eventrouter.SetupErrorReasonSetupError ("setup_error"): Subscriber.Setup failed (Phase 1)
//   - eventrouter.SetupErrorReasonReadyTimeout ("ready_timeout"): Ready not signaled within timeout (Phase 3)
//   - eventrouter.SetupErrorReasonPanic ("panic"): subscription goroutine panicked (Phase 2)
func (c *EventRouterCollector) RecordSetupError(cellID, topic, reason string) {
	if c == nil {
		return
	}
	c.setupErr.With(kernelmetrics.Labels{
		"cell":   cellID,
		"topic":  topic,
		"reason": reason,
	}).Inc()
}

// ObserveReadyWait records the duration waited for the Ready signal on cellID.
func (c *EventRouterCollector) ObserveReadyWait(cellID string, d time.Duration) {
	if c == nil {
		return
	}
	c.readyWait.With(kernelmetrics.Labels{"cell": cellID}).Observe(d.Seconds())
}

// RecordRuntimeError increments the runtime error counter for the given cell,
// topic, and reason. The reason argument is drawn from the closed set defined
// by the eventrouter package constants:
//   - eventrouter.RuntimeErrorReasonSubscribeFailure ("subscribe_failure"): SubscribeEntry failed (Phase 4)
//   - eventrouter.RuntimeErrorReasonReadyWaitTimeout ("ready_wait_timeout"): Ready not signaled within timeout (Phase 3)
//   - eventrouter.RuntimeErrorReasonRuntimeFault ("runtime_fault"): unclassified runtime fault (Phase 4)
func (c *EventRouterCollector) RecordRuntimeError(cellID, topic, reason string) {
	if c == nil {
		return
	}
	c.runtimeErr.With(kernelmetrics.Labels{
		"cell":   cellID,
		"topic":  topic,
		"reason": reason,
	}).Inc()
}
