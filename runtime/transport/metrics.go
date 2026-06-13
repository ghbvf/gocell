package transport

import (
	"context"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
)

// metricName / labelTransportMode are the frozen identifiers of the cross-cell
// transport request counter (observability.md §Cross-cell transport).
const (
	metricName         = "cell_transport_requests_total"
	labelTransportMode = "transport_mode"
)

// Metrics is the cross-cell transport metric collector. It owns the
// `cell_transport_requests_total{transport_mode}` counter, whose only label is
// the sealed binary [TransportMode] — so the label value set is closed by the
// type system (ADR D4: trace/metrics MUST distinguish in_proc vs remote, at a
// frozen low cardinality).
type Metrics struct {
	requests kernelmetrics.CounterVec
}

// NewMetrics registers the transport request counter on p. Registration is
// failable (duplicate name / invalid opts); callers treat the error as fatal at
// start-up.
func NewMetrics(p kernelmetrics.Provider) (*Metrics, error) {
	cv, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name:       metricName,
		Help:       "Total cross-cell sync (http) contract calls, by transport mode (in_proc | remote).",
		LabelNames: []string{labelTransportMode},
	})
	if err != nil {
		return nil, err
	}
	return &Metrics{requests: cv}, nil
}

// Record increments the transport request counter for mode. It is nil-safe: a
// nil *Metrics records nothing, so tests (and any composition path that omits a
// metrics provider) can run the transport without a registered collector. The
// mode argument is the sealed [TransportMode], so the label value is unforgeable.
func (m *Metrics) Record(ctx context.Context, mode TransportMode) {
	if m == nil {
		return
	}
	m.requests.With(kernelmetrics.Labels{labelTransportMode: mode.String()}).Inc(ctx)
}
