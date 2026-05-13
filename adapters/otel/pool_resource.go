package otel

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"

	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/kernel/observability/poolstats"
	"github.com/ghbvf/gocell/kernel/worker"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Metric names follow the OTel database semantic conventions:
//
//	https://opentelemetry.io/docs/specs/semconv/database/database-metrics/
//
// db.client.connection.count is a single UpDownCounter split by state="idle|used";
// db.client.connection.max is an UpDownCounter for configured pool capacity;
// db.client.connection.timeouts is a Counter tracking failed-acquire waits.
//
// SCOPE — this collector is for **database connection pools only**
// (adapters/postgres, adapters/redis). Do NOT pass the rabbitmq channel
// pool statter here: amqp channels inside a single TCP connection are not
// database connections, and emitting them through db.client.connection.*
// produces misleading dashboards. Use RegisterMessagingChannelMetrics
// (messaging_channel_collector.go) for that case.
const (
	metricNameConnCount    = "db.client.connection.count"
	metricNameConnMax      = "db.client.connection.max"
	metricNameConnTimeouts = "db.client.connection.timeouts"

	attrPoolName = "db.client.connection.pool.name"
	attrState    = "db.client.connection.state"

	stateIdle = "idle"
	stateUsed = "used"

	msgCreatePoolCounterFailed = "otel pool collector: create counter failed"
)

// NewPoolMetricsResource registers db.client.connection.* OTel observable
// gauges driven by the supplied statters and returns the registration as a
// kernel/lifecycle.ManagedResource. Bootstrap registers the resource via
// bootstrap.WithManagedResource and the OTel callback is unregistered as
// part of LIFO shutdown.
//
// Statter-less callers (len(statters) == 0) receive a no-op resource whose
// Close returns nil — callers can always wire the resource without a nil
// guard.
//
// Design note — why ManagedResource and not lifecycle.ContextCloser:
// bootstrap.WithManagedResource is the single bootstrap option that drives
// LIFO teardown for adapter-style components; using ContextCloser would
// require a separate bootstrap option just for this collector. The
// Checkers/Worker aspects are deliberately nil — OTel callback-based
// metric emission has no out-of-band health probe (the callback runs
// synchronously on every collect cycle; failures surface through OTel's
// own internal diagnostic handler) and no background goroutine.
//
// ref: kernel/lifecycle/managed_resource.go — three-aspect bundle
// (Checkers/Worker/Close); pool collector uses only Close.
// ref: opentelemetry-go metric/meter.go@main Registration.Unregister —
// canonical callback teardown used inside Close; idempotent per OTel SDK.
func NewPoolMetricsResource(meter otelmetric.Meter, statters []poolstats.Statter) (lifecycle.ManagedResource, error) {
	unregister, err := registerPoolCallbacks(meter, statters)
	if err != nil {
		return nil, err
	}
	return &poolMetricsResource{unregister: unregister}, nil
}

// poolMetricsResource adapts an OTel callback Registration to the
// kernel/lifecycle.ManagedResource contract.
type poolMetricsResource struct {
	unregister func() error
}

// Compile-time assertion: poolMetricsResource satisfies ManagedResource.
var _ lifecycle.ManagedResource = (*poolMetricsResource)(nil)

// Checkers returns nil: the OTel callback fires synchronously on each
// collect cycle. There is no out-of-band "are we healthy" probe — if the
// callback panics, OTel surfaces it through its own diagnostic channel.
func (*poolMetricsResource) Checkers() map[string]func(context.Context) error {
	return nil
}

// Worker returns nil: no background goroutine — emission is driven by
// the OTel reader's collect cycle.
func (*poolMetricsResource) Worker() worker.Worker { return nil }

// Close unregisters the OTel callback. ctx is accepted for ManagedResource
// contract symmetry; Registration.Unregister is synchronous and ignores it.
func (r *poolMetricsResource) Close(_ context.Context) error {
	return r.unregister()
}

// registerPoolCallbacks registers observable gauges on the supplied Meter
// and drives them from the given list of Statters on every collection
// cycle. The returned unregister function MUST be called at Stop to
// release the callback — otherwise the callback retains the statters
// and may outlive their owning pool. NewPoolMetricsResource is the only
// public caller; it ties the unregister to ManagedResource.Close.
//
// ref: opentelemetry-go metric/meter.go@main — Int64ObservableUpDownCounter
// + Meter.RegisterCallback is the canonical pattern for push-style gauges
// backed by push-style snapshots (as opposed to pull-style readers that
// would read directly during collection).
func registerPoolCallbacks(meter otelmetric.Meter, statters []poolstats.Statter) (func() error, error) {
	if meter == nil {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterOTelConfig,
			"otel pool collector: Meter is required")
	}
	if len(statters) == 0 {
		// Register nothing, but return a no-op unregister so callers can
		// always invoke Close without a nil check.
		return func() error { return nil }, nil
	}

	connCount, err := meter.Int64ObservableUpDownCounter(
		metricNameConnCount,
		otelmetric.WithDescription("Number of connections currently in the pool, partitioned by state."),
		otelmetric.WithUnit("{connection}"),
	)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterOTelInit,
			msgCreatePoolCounterFailed, err,
			errcode.WithDetails(slog.String("metric", metricNameConnCount)))
	}

	connMax, err := meter.Int64ObservableUpDownCounter(
		metricNameConnMax,
		otelmetric.WithDescription("Maximum number of connections the pool will allow."),
		otelmetric.WithUnit("{connection}"),
	)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterOTelInit,
			msgCreatePoolCounterFailed, err,
			errcode.WithDetails(slog.String("metric", metricNameConnMax)))
	}

	// db.client.connection.timeouts is a monotonically increasing Counter
	// — we read Snapshot.WaitCount on each callback (pgxpool's
	// EmptyAcquireCount, go-redis's Timeouts) which is already a cumulative
	// total, so an ObservableCounter lines up 1:1 with semantics.
	connTimeouts, err := meter.Int64ObservableCounter(
		metricNameConnTimeouts,
		otelmetric.WithDescription("Cumulative number of pool-acquire waits that timed out or short-circuited"+
			" (adapter-specific: pgxpool EmptyAcquireCount, go-redis Timeouts)."),
		otelmetric.WithUnit("{timeout}"),
	)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterOTelInit,
			msgCreatePoolCounterFailed, err,
			errcode.WithDetails(slog.String("metric", metricNameConnTimeouts)))
	}

	// Snapshot each statter during the callback; OTel calls this on every
	// collect cycle so the numbers always reflect the latest pool state.
	reg, err := meter.RegisterCallback(
		func(_ context.Context, o otelmetric.Observer) error {
			for _, s := range statters {
				snap := s.Snapshot()
				name := s.PoolName()
				poolAttr := attribute.String(attrPoolName, name)
				o.ObserveInt64(connCount, snap.IdleConns,
					otelmetric.WithAttributes(poolAttr, attribute.String(attrState, stateIdle)))
				o.ObserveInt64(connCount, snap.UsedConns,
					otelmetric.WithAttributes(poolAttr, attribute.String(attrState, stateUsed)))
				o.ObserveInt64(connMax, snap.MaxConns,
					otelmetric.WithAttributes(poolAttr))
				o.ObserveInt64(connTimeouts, snap.WaitCount,
					otelmetric.WithAttributes(poolAttr))
			}
			return nil
		},
		connCount,
		connMax,
		connTimeouts,
	)
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterOTelInit,
			"otel pool collector: register callback", err)
	}

	return reg.Unregister, nil
}
