package otel

import (
	"context"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/observability/poolstats"
	"go.opentelemetry.io/otel/attribute"
	otelmetric "go.opentelemetry.io/otel/metric"
)

// Messaging channel-pool metric names — gocell.* namespaced because
// OpenTelemetry's messaging semantic conventions do not currently define
// a first-class channel pool gauge (they focus on per-message
// attributes). Using an explicit, namespaced metric family makes intent
// unambiguous and avoids conflating with db.client.connection.*.
//
// ref: https://opentelemetry.io/docs/specs/semconv/messaging/ — defines
// `messaging.destination.*` and `messaging.operation.*` for message-level
// observation, but leaves client-side pool state to implementations.
const (
	metricNameChannelCount = "gocell.messaging.channel.count"
	metricNameChannelMax   = "gocell.messaging.channel.max"

	attrMessagingSystem = "messaging.system"
	attrChannelPoolName = "messaging.channel.pool.name"
	attrChannelState    = "messaging.channel.state"

	messagingSystemRabbitMQ = "rabbitmq"
)

// MessagingChannelStatter pairs a poolstats.Statter with the messaging
// system it belongs to. messagingSystem becomes the `messaging.system`
// attribute on emitted metrics so dashboards can pivot across brokers.
type MessagingChannelStatter struct {
	// System identifies the broker (e.g. "rabbitmq", "kafka"). Required.
	System string
	// Statter provides the snapshot. Required.
	Statter poolstats.Statter
}

// RegisterMessagingChannelMetrics registers two observable gauges for
// messaging client channel pools and drives them from the given statters
// on every collection cycle:
//
//	gocell.messaging.channel.count{messaging.system, pool.name, state=idle|used}
//	gocell.messaging.channel.max{messaging.system, pool.name}
//
// The caller supplies (System, Statter) tuples so one collector can track
// multiple brokers uniformly. Returns an unregister function that MUST be
// invoked at Stop to release the callback; a leaked callback would
// continue to read from snapshotters whose owning connections have been
// closed.
//
// ref: adapters/otel/pool_collector.go — same Meter.RegisterCallback
// pattern; the split exists so each metric family carries correct
// semantic-convention metadata (db vs messaging).
func RegisterMessagingChannelMetrics(meter otelmetric.Meter, statters []MessagingChannelStatter) (unregister func() error, err error) {
	if meter == nil {
		return nil, errcode.New(ErrAdapterOTelConfig,
			"otel messaging channel collector: Meter is required")
	}
	if len(statters) == 0 {
		return func() error { return nil }, nil
	}
	for i, s := range statters {
		if s.Statter == nil || s.System == "" {
			return nil, errcode.New(ErrAdapterOTelConfig,
				"otel messaging channel collector: statters[%d] missing System or Statter")
		}
		_ = i
	}

	chanCount, err := meter.Int64ObservableUpDownCounter(
		metricNameChannelCount,
		otelmetric.WithDescription("Number of broker channels currently in the pool, partitioned by state."),
		otelmetric.WithUnit("{channel}"),
	)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOTelInit,
			"otel messaging channel collector: create "+metricNameChannelCount, err)
	}
	chanMax, err := meter.Int64ObservableUpDownCounter(
		metricNameChannelMax,
		otelmetric.WithDescription("Maximum number of channels the client will pool per broker connection."),
		otelmetric.WithUnit("{channel}"),
	)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOTelInit,
			"otel messaging channel collector: create "+metricNameChannelMax, err)
	}

	reg, err := meter.RegisterCallback(
		func(ctx context.Context, o otelmetric.Observer) error {
			for _, s := range statters {
				snap := s.Statter.Snapshot()
				name := s.Statter.PoolName()
				baseAttrs := []attribute.KeyValue{
					attribute.String(attrMessagingSystem, s.System),
					attribute.String(attrChannelPoolName, name),
				}
				o.ObserveInt64(chanCount, snap.IdleConns,
					otelmetric.WithAttributes(append(baseAttrs, attribute.String(attrChannelState, "idle"))...))
				o.ObserveInt64(chanCount, snap.UsedConns,
					otelmetric.WithAttributes(append(baseAttrs, attribute.String(attrChannelState, "used"))...))
				o.ObserveInt64(chanMax, snap.MaxConns,
					otelmetric.WithAttributes(baseAttrs...))
			}
			return nil
		},
		chanCount,
		chanMax,
	)
	if err != nil {
		return nil, errcode.Wrap(ErrAdapterOTelInit,
			"otel messaging channel collector: register callback", err)
	}

	return reg.Unregister, nil
}

// MessagingSystemRabbitMQ is the canonical value for the
// `messaging.system` attribute emitted by this collector when the
// underlying statter comes from adapters/rabbitmq.
const MessagingSystemRabbitMQ = messagingSystemRabbitMQ
