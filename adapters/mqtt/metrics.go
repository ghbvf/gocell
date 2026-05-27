package mqtt

import (
	"context"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ConnectionCollector records MQTT connection-level metrics.
// Implementations must be safe for concurrent use.
type ConnectionCollector interface {
	// RecordReconnect increments the reconnect counter for this collector's cell.
	RecordReconnect(ctx context.Context)
}

// providerConnectionCollector implements ConnectionCollector via a provider-
// neutral metrics.Provider. Wired at the composition root.
//
// Metric:
//
//	mqtt_reconnect_total (counter, labels: cell)
//
// ref: adapters/rabbitmq/publisher_metrics.go — same inject-at-construction pattern.
type providerConnectionCollector struct {
	cellID    string
	reconnect metrics.CounterVec
}

var _ ConnectionCollector = (*providerConnectionCollector)(nil)

// NewProviderConnectionCollector registers mqtt_reconnect_total on p and
// returns a ConnectionCollector bound to cellID. cellID becomes the "cell" label.
//
// Returns error when p is nil, cellID is empty, or the Provider reports
// registration failure (e.g. duplicate metric names).
func NewProviderConnectionCollector(p metrics.Provider, cellID string) (ConnectionCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: metrics.Provider is required")
	}
	if cellID == "" {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: cellID is required for provider connection collector")
	}
	reconnect, err := p.CounterVec(metrics.CounterOpts{
		Name:       "mqtt_reconnect_total",
		Help:       "Total MQTT reconnect events observed by the adapter, by cell.",
		LabelNames: []string{"cell"},
	})
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"mqtt: register reconnect counter", err)
	}
	return &providerConnectionCollector{cellID: cellID, reconnect: reconnect}, nil
}

// RecordReconnect increments mqtt_reconnect_total{cell=c.cellID}.
func (c *providerConnectionCollector) RecordReconnect(ctx context.Context) {
	c.reconnect.With(metrics.Labels{"cell": c.cellID}).Inc(ctx)
}
