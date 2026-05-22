package prometheus_test

import (
	"strings"
	"testing"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	promadapter "github.com/ghbvf/gocell/adapters/prometheus"
)

func TestRegisterOrReuseCounter_FreshRegister(t *testing.T) {
	reg := prom.NewRegistry()
	opts := prom.CounterOpts{
		Namespace: "test",
		Name:      "fresh_counter_total",
		Help:      "test fresh",
	}

	c, err := promadapter.RegisterOrReuseCounter(reg, opts)
	require.NoError(t, err)
	require.NotNil(t, c)

	// Verify the counter is functional.
	c.Inc()
	c.Add(2.0)
}

func TestRegisterOrReuseCounter_ReuseExisting(t *testing.T) {
	reg := prom.NewRegistry()
	opts := prom.CounterOpts{
		Namespace: "test",
		Name:      "reuse_counter_total",
		Help:      "test reuse",
	}

	first, err := promadapter.RegisterOrReuseCounter(reg, opts)
	require.NoError(t, err)
	require.NotNil(t, first)

	// Second call with same opts on same registry — must succeed and return a counter.
	second, err := promadapter.RegisterOrReuseCounter(reg, opts)
	require.NoError(t, err)
	require.NotNil(t, second)

	// Increment via first. If reuse branch erroneously returned a fresh unregistered
	// counter, testutil.ToFloat64(second) would gather 0 (not 2) because second
	// wouldn't be the registry-known collector.
	first.Inc()
	first.Inc()

	got := testutil.ToFloat64(second)
	require.Equal(t, 2.0, got,
		"reuse branch must return the same registry-registered collector as first; "+
			"got %v from registry, expected 2", got)
}

func TestNewCounter_Smoke(t *testing.T) {
	c := promadapter.NewCounter(prom.CounterOpts{Name: "pub_smoke_counter", Help: "smoke"})
	require.NotNil(t, c)
	c.Inc()
}

func TestNewCounterVec_Smoke(t *testing.T) {
	cv := promadapter.NewCounterVec(prom.CounterOpts{Name: "pub_smoke_counter_vec", Help: "smoke"}, []string{"label"})
	require.NotNil(t, cv)
	cv.WithLabelValues("x").Inc()
}

func TestNewGauge_Smoke(t *testing.T) {
	g := promadapter.NewGauge(prom.GaugeOpts{Name: "pub_smoke_gauge", Help: "smoke"})
	require.NotNil(t, g)
	g.Set(1.0)
}

func TestNewGaugeFunc_Smoke(t *testing.T) {
	called := false
	gf := promadapter.NewGaugeFunc(prom.GaugeOpts{Name: "pub_smoke_gauge_func", Help: "smoke"}, func() float64 {
		called = true
		return 7.0
	})
	require.NotNil(t, gf)

	// ToFloat64 triggers a full Gather+Write cycle which invokes the callback.
	got := testutil.ToFloat64(gf)
	assert.True(t, called, "GaugeFunc callback must have been invoked by ToFloat64")
	assert.Equal(t, 7.0, got, "GaugeFunc must return the value from the callback")
}

func TestRegisterOrReuseCounter_NonCounterCollision(t *testing.T) {
	reg := prom.NewRegistry()
	// Use the exact same fqName + help so Prometheus surfaces AlreadyRegisteredError
	// (descriptor match). Use a Summary which does NOT implement prom.Counter, so the
	// "existing collector is not a Counter" branch in RegisterOrReuseCounter is triggered.
	const name = "collision_total"
	const help = "shared help for collision test"

	// Register a Summary first — it does not implement prom.Counter.
	summary := prom.NewSummary(prom.SummaryOpts{
		Namespace: "test",
		Name:      name,
		Help:      help,
	})
	require.NoError(t, reg.Register(summary))

	// Attempt to register a Counter with same name + help — must fail with "not a Counter".
	opts := prom.CounterOpts{
		Namespace: "test",
		Name:      name,
		Help:      help,
	}
	c, err := promadapter.RegisterOrReuseCounter(reg, opts)
	require.Error(t, err)
	assert.Nil(t, c)
	assert.True(t, strings.Contains(err.Error(), "not a Counter"),
		"expected error to mention 'not a Counter', got: %s", err.Error())
}
