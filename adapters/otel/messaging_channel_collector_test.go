package otel_test

import (
	"context"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	gcotel "github.com/ghbvf/gocell/adapters/otel"
	"github.com/ghbvf/gocell/framework/kernel/observability/poolstats"
)

// messagingChannelAggregated holds the per-state and per-pool-max aggregations
// collected from ResourceMetrics for messaging channel metrics.
type messagingChannelAggregated struct {
	perState           map[string]int64
	maxPerPool         map[string]int64
	sawMessagingSystem bool
}

// aggregateMessagingChannelMetrics walks rm and aggregates gocell.messaging.channel.*
// data points into a messagingChannelAggregated. Called from the test to separate
// aggregation from assertion and reduce cognitive complexity (S3776 CC=29).
func aggregateMessagingChannelMetrics(t *testing.T, rm metricdata.ResourceMetrics) messagingChannelAggregated {
	t.Helper()
	result := messagingChannelAggregated{
		perState:   map[string]int64{},
		maxPerPool: map[string]int64{},
	}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "gocell.messaging.channel.count" && m.Name != "gocell.messaging.channel.max" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("metric %s is not Sum[int64], got %T", m.Name, m.Data)
			}
			aggregateDataPoints(t, &result, m.Name, sum.DataPoints)
		}
	}
	return result
}

// aggregateDataPoints processes one metric's data points into the aggregation result.
func aggregateDataPoints(t *testing.T, result *messagingChannelAggregated, metricName string, dps []metricdata.DataPoint[int64]) {
	t.Helper()
	for _, dp := range dps {
		if v, ok := dp.Attributes.Value("messaging.system"); ok && v.AsString() == "rabbitmq" {
			result.sawMessagingSystem = true
		}
		pool, _ := dp.Attributes.Value("messaging.channel.pool.name")
		if metricName == "gocell.messaging.channel.count" {
			state, _ := dp.Attributes.Value("messaging.channel.state")
			result.perState[pool.AsString()+":"+state.AsString()] = dp.Value
		} else {
			result.maxPerPool[pool.AsString()] = dp.Value
		}
	}
}

func TestRegisterMessagingChannelMetrics_EmitsPerStateAndMax(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	meter := mp.Meter("gocell.test.messaging")

	statters := []gcotel.MessagingChannelStatter{
		{
			System: gcotel.MessagingSystemRabbitMQ,
			Statter: staticStatter{
				name: "rmq-outbox",
				snap: poolstats.Snapshot{TotalConns: 8, IdleConns: 3, UsedConns: 5, MaxConns: 8},
			},
		},
	}
	unreg, err := gcotel.RegisterMessagingChannelMetrics(meter, statters)
	if err != nil {
		t.Fatalf("RegisterMessagingChannelMetrics: %v", err)
	}
	t.Cleanup(func() { _ = unreg() })

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	agg := aggregateMessagingChannelMetrics(t, rm)

	if !agg.sawMessagingSystem {
		t.Fatal("messaging.system attribute missing; broker-pivoting dashboard would break")
	}
	if agg.perState["rmq-outbox:idle"] != 3 || agg.perState["rmq-outbox:used"] != 5 {
		t.Errorf("rmq-outbox idle/used = %d/%d, want 3/5", agg.perState["rmq-outbox:idle"], agg.perState["rmq-outbox:used"])
	}
	if agg.maxPerPool["rmq-outbox"] != 8 {
		t.Errorf("rmq-outbox max = %d, want 8", agg.maxPerPool["rmq-outbox"])
	}
}

func TestRegisterMessagingChannelMetrics_EmptyAndNilGuards(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	meter := mp.Meter("x")

	// Empty slice → no-op unregister, no error.
	unreg, err := gcotel.RegisterMessagingChannelMetrics(meter, nil)
	if err != nil {
		t.Fatalf("empty: %v", err)
	}
	if err := unreg(); err != nil {
		t.Fatalf("no-op unregister: %v", err)
	}

	// Nil Meter rejected.
	if _, err := gcotel.RegisterMessagingChannelMetrics(nil, []gcotel.MessagingChannelStatter{
		{System: "rabbitmq", Statter: staticStatter{name: "x"}},
	}); err == nil {
		t.Fatal("nil meter must be rejected")
	}

	// Missing System rejected.
	if _, err := gcotel.RegisterMessagingChannelMetrics(meter, []gcotel.MessagingChannelStatter{
		{Statter: staticStatter{name: "x"}},
	}); err == nil {
		t.Fatal("missing System must be rejected")
	}

	// Missing Statter rejected.
	if _, err := gcotel.RegisterMessagingChannelMetrics(meter, []gcotel.MessagingChannelStatter{
		{System: "rabbitmq"},
	}); err == nil {
		t.Fatal("missing Statter must be rejected")
	}
}
