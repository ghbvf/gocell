package otel_test

import (
	"context"
	"testing"

	gcotel "github.com/ghbvf/gocell/adapters/otel"
	"github.com/ghbvf/gocell/runtime/observability/poolstats"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

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

	perState := map[string]int64{}
	maxPerPool := map[string]int64{}
	sawMessagingSystem := false
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "gocell.messaging.channel.count" && m.Name != "gocell.messaging.channel.max" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("metric %s is not Sum[int64], got %T", m.Name, m.Data)
			}
			for _, dp := range sum.DataPoints {
				if v, ok := dp.Attributes.Value("messaging.system"); ok && v.AsString() == "rabbitmq" {
					sawMessagingSystem = true
				}
				pool, _ := dp.Attributes.Value("messaging.channel.pool.name")
				if m.Name == "gocell.messaging.channel.count" {
					state, _ := dp.Attributes.Value("messaging.channel.state")
					perState[pool.AsString()+":"+state.AsString()] = dp.Value
				} else {
					maxPerPool[pool.AsString()] = dp.Value
				}
			}
		}
	}

	if !sawMessagingSystem {
		t.Fatal("messaging.system attribute missing; broker-pivoting dashboard would break")
	}
	if perState["rmq-outbox:idle"] != 3 || perState["rmq-outbox:used"] != 5 {
		t.Errorf("rmq-outbox idle/used = %d/%d, want 3/5", perState["rmq-outbox:idle"], perState["rmq-outbox:used"])
	}
	if maxPerPool["rmq-outbox"] != 8 {
		t.Errorf("rmq-outbox max = %d, want 8", maxPerPool["rmq-outbox"])
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
