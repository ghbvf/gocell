package metrics

import (
	"context"
	"fmt"
	"time"

	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/runtime/eventrouter"
)

// EventRouterCollector registers event-router lifecycle metrics:
//   - event_router_subscriptions_active{cell} (Gauge): active subscription count
//   - event_router_setup_errors_total{cell,topic,reason} (Counter): setup-phase failures (Phase 1-3)
//   - event_router_ready_wait_seconds{cell} (Histogram): time waited for Ready signal
//   - event_router_runtime_errors_total{cell,topic,reason} (Counter): runtime-phase faults (Phase 4)
//
// Histogram buckets for ready_wait_seconds are: 0.001, 0.01, 0.1, 0.5, 1, 5, 30 seconds.
// Ready usually completes in <100ms; bootstrap timeout is 30s, so the upper bound
// covers the full expected range.
//
// PR #593 review fix-up (P2#3): the setup vs runtime metrics partition the
// router lifecycle by phase, NOT by error variety. event_router_setup_errors_total
// owns Phase 1–3 failures; event_router_runtime_errors_total owns Phase 4
// faults. The reason label set is therefore disjoint across the two metrics:
//
//   - event_router_setup_errors_total reasons:
//     "setup_error":       Subscriber.Setup failed (Phase 1)
//     "panic":             Subscribe goroutine panicked (Phase 2)
//     "ready_timeout":     Ready not signaled within timeout (Phase 3)
//     "subscribe_failure": SubscribeEntry failed before Running() (Phase 3)
//   - event_router_runtime_errors_total reasons:
//     "runtime_fault":     any fault detected in Phase 4 (after Running())
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
		Name: "event_router_setup_errors_total",
		Help: "Total number of event router subscription setup-phase failures (Phase 1-3), " +
			"partitioned by cell, topic, and reason. reason is a closed set: " +
			"setup_error | panic | ready_timeout | subscribe_failure.",
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
		Name: "event_router_runtime_errors_total",
		Help: "Total number of event router runtime-phase faults (Phase 4, after Running()), " +
			"partitioned by cell, topic, and reason. reason is a closed set: runtime_fault.",
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
func (c *EventRouterCollector) IncSubscriptionActive(ctx context.Context, cellID string) {
	if c == nil {
		return
	}
	c.active.With(kernelmetrics.Labels{"cell": cellID}).Inc(ctx)
}

// DecSubscriptionActive decrements the active subscription gauge for cellID.
func (c *EventRouterCollector) DecSubscriptionActive(ctx context.Context, cellID string) {
	if c == nil {
		return
	}
	c.active.With(kernelmetrics.Labels{"cell": cellID}).Dec(ctx)
}

// RecordSetupError increments the setup error counter for the given cell, topic
// and reason. The reason argument is drawn from the closed set defined by the
// eventrouter package constants:
//   - eventrouter.SetupErrorReasonSetupError ("setup_error"): Subscriber.Setup failed (Phase 1)
//   - eventrouter.SetupErrorReasonPanic ("panic"): subscription goroutine panicked (Phase 2)
//   - eventrouter.SetupErrorReasonReadyTimeout ("ready_timeout"): Ready not signaled within timeout (Phase 3)
//   - eventrouter.SetupErrorReasonSubscribeFailure ("subscribe_failure"): SubscribeEntry failed before Running() (Phase 3)
func (c *EventRouterCollector) RecordSetupError(ctx context.Context, cellID, topic, reason string) {
	if c == nil {
		return
	}
	c.setupErr.With(kernelmetrics.Labels{
		"cell":   cellID,
		"topic":  topic,
		"reason": reason,
	}).Inc(ctx)
}

// ObserveReadyWait records the duration waited for the Ready signal on cellID.
func (c *EventRouterCollector) ObserveReadyWait(ctx context.Context, cellID string, d time.Duration) {
	if c == nil {
		return
	}
	c.readyWait.With(kernelmetrics.Labels{"cell": cellID}).Observe(ctx, d.Seconds())
}

// RecordRuntimeError increments the runtime error counter for the given cell,
// topic, and reason. The reason argument is drawn from the closed set defined
// by the eventrouter package constants:
//   - eventrouter.RuntimeErrorReasonRuntimeFault ("runtime_fault"): any fault detected in Phase 4
func (c *EventRouterCollector) RecordRuntimeError(ctx context.Context, cellID, topic string, reason eventrouter.RuntimeErrorReason) {
	if c == nil {
		return
	}
	c.runtimeErr.With(kernelmetrics.Labels{
		"cell":   cellID,
		"topic":  topic,
		"reason": string(reason),
	}).Inc(ctx)
}
