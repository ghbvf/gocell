package otel_test

import (
	"context"
	"testing"

	gcotel "github.com/ghbvf/gocell/adapters/otel"
	"github.com/ghbvf/gocell/runtime/observability/poolstats"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// staticStatter is a test double: returns the supplied fixed snapshot on
// every call, independent of time or concurrent writes.
type staticStatter struct {
	name string
	snap poolstats.Snapshot
}

func (s staticStatter) PoolName() string             { return s.name }
func (s staticStatter) Snapshot() poolstats.Snapshot { return s.snap }

func TestRegisterPoolMetrics_EmitsIdleAndUsed(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	meter := mp.Meter("gocell.test")

	statters := []poolstats.Statter{
		staticStatter{
			name: "pg-main",
			snap: poolstats.Snapshot{TotalConns: 10, IdleConns: 3, UsedConns: 7, MaxConns: 20, WaitCount: 1},
		},
		staticStatter{
			name: "redis-main",
			snap: poolstats.Snapshot{TotalConns: 5, IdleConns: 2, UsedConns: 3, MaxConns: 8, WaitCount: 0},
		},
	}
	unreg, err := gcotel.RegisterPoolMetrics(meter, statters)
	if err != nil {
		t.Fatalf("RegisterPoolMetrics: %v", err)
	}
	t.Cleanup(func() { _ = unreg() })

	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("Collect: %v", err)
	}

	idleUsed := map[string]int64{}
	maxPerPool := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name != "db.client.connection.count" && m.Name != "db.client.connection.max" {
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("metric %s is not Sum[int64], got %T", m.Name, m.Data)
			}
			for _, dp := range sum.DataPoints {
				pool, _ := dp.Attributes.Value("db.client.connection.pool.name")
				state, hasState := dp.Attributes.Value("db.client.connection.state")
				if m.Name == "db.client.connection.count" && hasState {
					idleUsed[pool.AsString()+":"+state.AsString()] = dp.Value
				}
				if m.Name == "db.client.connection.max" {
					maxPerPool[pool.AsString()] = dp.Value
				}
			}
		}
	}

	if idleUsed["pg-main:idle"] != 3 || idleUsed["pg-main:used"] != 7 {
		t.Errorf("pg-main idle/used = %d/%d, want 3/7", idleUsed["pg-main:idle"], idleUsed["pg-main:used"])
	}
	if idleUsed["redis-main:idle"] != 2 || idleUsed["redis-main:used"] != 3 {
		t.Errorf("redis-main idle/used = %d/%d, want 2/3", idleUsed["redis-main:idle"], idleUsed["redis-main:used"])
	}
	if maxPerPool["pg-main"] != 20 || maxPerPool["redis-main"] != 8 {
		t.Errorf("max per pool mismatch: %+v", maxPerPool)
	}
}

func TestRegisterPoolMetrics_EmptySlice_NoOpUnregister(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })

	unreg, err := gcotel.RegisterPoolMetrics(mp.Meter("x"), nil)
	if err != nil {
		t.Fatalf("RegisterPoolMetrics: %v", err)
	}
	if err := unreg(); err != nil {
		t.Fatalf("no-op unregister should return nil, got %v", err)
	}
}

func TestRegisterPoolMetrics_NilMeter_Error(t *testing.T) {
	if _, err := gcotel.RegisterPoolMetrics(nil, []poolstats.Statter{
		staticStatter{name: "x"},
	}); err == nil {
		t.Fatal("nil meter must be rejected")
	}
}
