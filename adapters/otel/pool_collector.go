package otel

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/observability/poolstats"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

// Metric names follow the OTel database semantic conventions:
//   https://opentelemetry.io/docs/specs/semconv/database/database-metrics/
// db.client.connection.count is a single UpDownCounter split by state="idle|used";
// db.client.connection.max is an UpDownCounter for configured pool capacity.
const (
	metricNameConnCount = "db.client.connection.count"
	metricNameConnMax   = "db.client.connection.max"

	attrPoolName = "db.client.connection.pool.name"
	attrState    = "db.client.connection.state"

	stateIdle = "idle"
	stateUsed = "used"
)

// RegisterPoolMetrics registers observable gauges on the supplied Meter
// and drives them from the given list of Statters on every collection
// cycle. The returned unregister function MUST be called at Stop to
// release the callback — otherwise the callback retains the statters
// and may outlive their owning pool.
//
// ref: opentelemetry-go metric/meter.go@main — Int64ObservableUpDownCounter
// + Meter.RegisterCallback is the canonical pattern for push-style gauges
// backed by push-style snapshots (as opposed to pull-style readers that
// would read directly during collection).
func RegisterPoolMetrics(meter otelmetric.Meter, statters []poolstats.Statter) (unregister func() error, err error) {
	if meter == nil {
		return nil, errcode.New(ErrAdapterOTelConfig,
			"otel pool collector: Meter is required")
	}
	if len(statters) == 0 {
		// Register nothing, but return a no-op unregister so callers can
		// always `defer unregister()` without a nil check.
		return func() error { return nil }, nil
	}

	connCount, err := meter.Int64ObservableUpDownCounter(
		metricNameConnCount,
		otelmetric.WithDescription("Number of connections currently in the pool, partitioned by state."),
		otelmetric.WithUnit("{connection}"),
	)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOTelInit,
			"otel pool collector: create "+metricNameConnCount, err)
	}

	connMax, err := meter.Int64ObservableUpDownCounter(
		metricNameConnMax,
		otelmetric.WithDescription("Maximum number of connections the pool will allow."),
		otelmetric.WithUnit("{connection}"),
	)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOTelInit,
			"otel pool collector: create "+metricNameConnMax, err)
	}

	// Snapshot each statter during the callback; OTel calls this on every
	// collect cycle so the numbers always reflect the latest pool state.
	reg, err := meter.RegisterCallback(
		func(ctx context.Context, o otelmetric.Observer) error {
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
			}
			return nil
		},
		connCount,
		connMax,
	)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOTelInit,
			"otel pool collector: register callback", err)
	}

	return reg.Unregister, nil
}
