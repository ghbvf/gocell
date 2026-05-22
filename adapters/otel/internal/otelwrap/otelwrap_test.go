package otelwrap_test

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/metric/noop"

	"github.com/ghbvf/gocell/adapters/otel/internal/otelwrap"
)

func TestFloat64Counter(t *testing.T) {
	meter := noop.NewMeterProvider().Meter("smoke")
	c, err := otelwrap.Float64Counter(meter, "smoke_counter")
	require.NoError(t, err)
	require.NotNil(t, c)
}

func TestFloat64Gauge(t *testing.T) {
	meter := noop.NewMeterProvider().Meter("smoke")
	g, err := otelwrap.Float64Gauge(meter, "smoke_gauge")
	require.NoError(t, err)
	require.NotNil(t, g)
}

func TestFloat64Histogram(t *testing.T) {
	meter := noop.NewMeterProvider().Meter("smoke")
	h, err := otelwrap.Float64Histogram(meter, "smoke_histogram")
	require.NoError(t, err)
	require.NotNil(t, h)
}

// TestAllConstructors_Smoke mirrors promwrap's TestAllConstructorsDescribe:
// table-driven verification that every otelwrap synchronous constructor
// returns a non-nil instrument and reports no error against a noop meter.
// Surface-drift detection lives in archtest TestMetricsFunnel_SymbolSentinel;
// this test pins the noop-meter happy path for all currently-funneled
// instrument kinds.
func TestAllConstructors_Smoke(t *testing.T) {
	meter := noop.NewMeterProvider().Meter("smoke-table")

	tests := []struct {
		name        string
		constructor func() (any, error)
	}{
		{
			name: "Float64Counter",
			constructor: func() (any, error) {
				return otelwrap.Float64Counter(meter, "tbl_counter")
			},
		},
		{
			name: "Float64Gauge",
			constructor: func() (any, error) {
				return otelwrap.Float64Gauge(meter, "tbl_gauge")
			},
		},
		{
			name: "Float64Histogram",
			constructor: func() (any, error) {
				return otelwrap.Float64Histogram(meter, "tbl_histogram")
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			inst, err := tc.constructor()
			require.NoError(t, err)
			require.NotNil(t, inst)
		})
	}
}
