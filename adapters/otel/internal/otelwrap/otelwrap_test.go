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
