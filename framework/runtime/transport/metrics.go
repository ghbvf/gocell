package transport

import (
	"context"

	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
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
//
// transport_mode is the ONLY label, deliberately: the D4 requirement is the
// in_proc-vs-remote distinction, and a caller-cell dimension is NOT reliably
// available at the dispatch site — a CellTransport call often originates from an
// event-consumer goroutine (e.g. accesscore's config refetch) that carries no
// HTTP cell-attribution ctx, so a `cell` label would degrade to the sentinel and
// add cardinality without attribution. Per-link attribution, if needed, is a
// US5-era enhancement (richer ctx threading), not a D4 gap.
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
// metrics provider) can run the transport without a registered collector.
//
// Fail-closed on the label set: an UNregistered mode (a forged/zero-value
// TransportMode, whose String() renders as the [TransportModeUnknown] sentinel)
// is NEVER recorded — so the binary closed set {in_proc, remote} cannot be
// polluted. The sealed type (no external 3rd value) + this record-point guard +
// the allTransportModes registry triple-close the label value set.
func (m *Metrics) Record(ctx context.Context, mode TransportMode) {
	if m == nil {
		return
	}
	if !mode.isRegistered() {
		return
	}
	m.requests.With(kernelmetrics.Labels{labelTransportMode: mode.String()}).Inc(ctx)
}
