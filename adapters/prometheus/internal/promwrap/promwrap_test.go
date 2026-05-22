package promwrap_test

import (
	"testing"

	prom "github.com/prometheus/client_golang/prometheus"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/adapters/prometheus/internal/promwrap"
)

func TestNewCounter(t *testing.T) {
	c := promwrap.NewCounter(prom.CounterOpts{Name: "smoke_counter", Help: "smoke"})
	require.NotNil(t, c)
	c.Inc()
	c.Add(2.0)
}

func TestNewCounterVec(t *testing.T) {
	cv := promwrap.NewCounterVec(prom.CounterOpts{Name: "smoke_counter_vec", Help: "smoke"}, []string{"label"})
	require.NotNil(t, cv)

	reg := prom.NewRegistry()
	require.NoError(t, reg.Register(cv))

	ch := make(chan *prom.Desc, 1)
	cv.Describe(ch)
	require.NotNil(t, <-ch)

	cv.WithLabelValues("val").Inc()
}

func TestNewGaugeVec(t *testing.T) {
	gv := promwrap.NewGaugeVec(prom.GaugeOpts{Name: "smoke_gauge_vec", Help: "smoke"}, []string{"label"})
	require.NotNil(t, gv)

	reg := prom.NewRegistry()
	require.NoError(t, reg.Register(gv))

	ch := make(chan *prom.Desc, 1)
	gv.Describe(ch)
	require.NotNil(t, <-ch)

	gv.WithLabelValues("val").Set(42)
}

func TestNewHistogramVec(t *testing.T) {
	hv := promwrap.NewHistogramVec(
		prom.HistogramOpts{Name: "smoke_histogram_vec", Help: "smoke"},
		[]string{"label"},
	)
	require.NotNil(t, hv)

	reg := prom.NewRegistry()
	require.NoError(t, reg.Register(hv))

	ch := make(chan *prom.Desc, 1)
	hv.Describe(ch)
	require.NotNil(t, <-ch)

	hv.WithLabelValues("val").Observe(1.5)
}

// Table-driven smoke: all four constructors return non-nil and Describe does not panic.
func TestAllConstructorsDescribe(t *testing.T) {
	tests := []struct {
		name      string
		collector func() prom.Collector
	}{
		{
			name: "Counter",
			collector: func() prom.Collector {
				return promwrap.NewCounter(prom.CounterOpts{Name: "tbl_counter", Help: "tbl"})
			},
		},
		{
			name: "CounterVec",
			collector: func() prom.Collector {
				return promwrap.NewCounterVec(prom.CounterOpts{Name: "tbl_counter_vec", Help: "tbl"}, []string{"l"})
			},
		},
		{
			name: "GaugeVec",
			collector: func() prom.Collector {
				return promwrap.NewGaugeVec(prom.GaugeOpts{Name: "tbl_gauge_vec", Help: "tbl"}, []string{"l"})
			},
		},
		{
			name: "HistogramVec",
			collector: func() prom.Collector {
				return promwrap.NewHistogramVec(prom.HistogramOpts{Name: "tbl_histogram_vec", Help: "tbl"}, []string{"l"})
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			c := tc.collector()
			require.NotNil(t, c)

			ch := make(chan *prom.Desc, 10)
			require.NotPanics(t, func() { c.Describe(ch) })
			close(ch)

			// At least one descriptor must be emitted.
			var count int
			for range ch {
				count++
			}
			require.Greater(t, count, 0, "expected at least one descriptor")
		})
	}
}
