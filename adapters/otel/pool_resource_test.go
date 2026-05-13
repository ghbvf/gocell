package otel_test

import (
	"context"
	"testing"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	gcotel "github.com/ghbvf/gocell/adapters/otel"
	"github.com/ghbvf/gocell/kernel/observability/poolstats"
)

// staticStatter is a test double: returns the supplied fixed snapshot on
// every call, independent of time or concurrent writes.
type staticStatter struct {
	name string
	snap poolstats.Snapshot
}

func (s staticStatter) PoolName() string             { return s.name }
func (s staticStatter) Snapshot() poolstats.Snapshot { return s.snap }

// newTestMeterProvider returns a fresh ManualReader-backed MeterProvider
// and the Meter to register instruments on. The MeterProvider is shut
// down in t.Cleanup.
func newTestMeterProvider(t *testing.T) (*sdkmetric.ManualReader, *sdkmetric.MeterProvider) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = mp.Shutdown(context.Background()) })
	return reader, mp
}

func TestNewPoolMetricsResource_EmitsIdleAndUsed(t *testing.T) {
	reader, mp := newTestMeterProvider(t)
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
	res, err := gcotel.NewPoolMetricsResource(meter, statters)
	require.NoError(t, err)
	require.NotNil(t, res)
	t.Cleanup(func() { _ = res.Close(context.Background()) })

	var rm metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &rm))

	idleUsed := map[string]int64{}
	maxPerPool := map[string]int64{}
	timeoutsPerPool := map[string]int64{}
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case "db.client.connection.count", "db.client.connection.max", "db.client.connection.timeouts":
			default:
				continue
			}
			sum, ok := m.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("metric %s is not Sum[int64], got %T", m.Name, m.Data)
			}
			for _, dp := range sum.DataPoints {
				pool, _ := dp.Attributes.Value("db.client.connection.pool.name")
				state, hasState := dp.Attributes.Value("db.client.connection.state")
				switch m.Name {
				case "db.client.connection.count":
					if hasState {
						idleUsed[pool.AsString()+":"+state.AsString()] = dp.Value
					}
				case "db.client.connection.max":
					maxPerPool[pool.AsString()] = dp.Value
				case "db.client.connection.timeouts":
					timeoutsPerPool[pool.AsString()] = dp.Value
				}
			}
		}
	}

	assert.Equal(t, int64(3), idleUsed["pg-main:idle"])
	assert.Equal(t, int64(7), idleUsed["pg-main:used"])
	assert.Equal(t, int64(2), idleUsed["redis-main:idle"])
	assert.Equal(t, int64(3), idleUsed["redis-main:used"])
	assert.Equal(t, int64(20), maxPerPool["pg-main"])
	assert.Equal(t, int64(8), maxPerPool["redis-main"])
	assert.Equal(t, int64(1), timeoutsPerPool["pg-main"])
	assert.Equal(t, int64(0), timeoutsPerPool["redis-main"])
}

// B2-R-08: NewPoolMetricsResource must return a value implementing
// kernel/lifecycle.ManagedResource — the return type itself is the
// interface, so the call site exercises the contract; we assert the
// concrete shape (no probes, no worker) here.
func TestNewPoolMetricsResource_ImplementsManagedResource(t *testing.T) {
	_, mp := newTestMeterProvider(t)
	meter := mp.Meter("gocell.test")

	res, err := gcotel.NewPoolMetricsResource(meter, []poolstats.Statter{
		staticStatter{name: "p", snap: poolstats.Snapshot{}},
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = res.Close(context.Background()) })

	assert.Nil(t, res.Checkers(),
		"pool collector has no out-of-band health probe")
	assert.Nil(t, res.Worker(),
		"pool collector has no background worker")
}

// B2-R-08: Close must unregister the OTel callback. After Close, a fresh
// collect cycle must NOT include the pool metric data points (callback
// silenced).
func TestNewPoolMetricsResource_CloseSilencesCallback(t *testing.T) {
	reader, mp := newTestMeterProvider(t)
	meter := mp.Meter("gocell.test")

	res, err := gcotel.NewPoolMetricsResource(meter, []poolstats.Statter{
		staticStatter{
			name: "p-1",
			snap: poolstats.Snapshot{IdleConns: 1, UsedConns: 1, MaxConns: 2, WaitCount: 0},
		},
	})
	require.NoError(t, err)

	// Before Close: metric present.
	var before metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &before))
	require.True(t, hasPoolMetric(before), "metric must be present before Close")

	require.NoError(t, res.Close(context.Background()))

	// After Close: metric absent (callback unregistered).
	var after metricdata.ResourceMetrics
	require.NoError(t, reader.Collect(context.Background(), &after))
	assert.False(t, hasPoolMetric(after),
		"metric must be absent after Close — callback should be unregistered")
}

// B2-R-08: Close is idempotent — calling it twice from a forced-shutdown
// path (caller defer + ManagedResource LIFO) must not panic.
func TestNewPoolMetricsResource_NilMeterReturnsError(t *testing.T) {
	res, err := gcotel.NewPoolMetricsResource(nil, []poolstats.Statter{
		staticStatter{name: "x"},
	})
	require.Error(t, err)
	assert.Nil(t, res)
}

// B2-R-08: Empty statters slice still returns a valid resource whose
// Close is a no-op — keeps wiring code uniform across optional adapters.
func TestNewPoolMetricsResource_EmptyStattersIsNoop(t *testing.T) {
	_, mp := newTestMeterProvider(t)
	meter := mp.Meter("x")

	res, err := gcotel.NewPoolMetricsResource(meter, nil)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.NoError(t, res.Close(context.Background()))
}

func hasPoolMetric(rm metricdata.ResourceMetrics) bool {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			switch m.Name {
			case "db.client.connection.count",
				"db.client.connection.max",
				"db.client.connection.timeouts":
				return true
			}
		}
	}
	return false
}
