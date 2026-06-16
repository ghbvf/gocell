package transport

import (
	"context"

	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
)

// metricName / labelTransportMode / labelOutcome are the frozen identifiers of
// the cross-cell transport request counter (observability.md §Cross-cell transport).
const (
	metricName         = "cell_transport_requests_total"
	labelTransportMode = "transport_mode"
	labelOutcome       = "outcome"
)

// Metrics is the cross-cell transport metric collector. It owns the
// `cell_transport_requests_total{transport_mode, outcome}` counter, whose labels
// are the sealed [TransportMode] and [TransportOutcome] — so both value sets are
// closed by the type system (ADR D4: trace/metrics MUST distinguish in_proc vs
// remote, at a frozen low cardinality; #1966 review P2.6 adds the outcome dimension
// so failed dispatches are counted, not only the success path).
//
// transport_mode (binary) and outcome (success + bounded failure kinds) are the
// only labels, deliberately: a caller-cell dimension is NOT reliably available at
// the dispatch site — a CellTransport call often originates from an event-consumer
// goroutine (e.g. accesscore's config refetch) that carries no HTTP cell-attribution
// ctx, so a `cell` label would degrade to the sentinel and add cardinality without
// attribution. Per-link attribution, if needed, is a later enhancement (richer ctx
// threading), not a D4 gap. error.type detail beyond the bounded outcome kind stays
// on the trace span, not the metric.
type Metrics struct {
	requests kernelmetrics.CounterVec
}

// NewMetrics registers the transport request counter on p. Registration is
// failable (duplicate name / invalid opts); callers treat the error as fatal at
// start-up.
func NewMetrics(p kernelmetrics.Provider) (*Metrics, error) {
	cv, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name:       metricName,
		Help:       "Total cross-cell sync (http) contract calls, by transport_mode (in_proc | remote) and outcome (success | failure kinds).",
		LabelNames: []string{labelTransportMode, labelOutcome},
	})
	if err != nil {
		return nil, err
	}
	return &Metrics{requests: cv}, nil
}

// Record increments the transport request counter for (mode, outcome). It is
// nil-safe: a nil *Metrics records nothing, so tests (and any composition path
// that omits a metrics provider) can run the transport without a registered
// collector.
//
// Fail-closed on the label set: an UNregistered mode OR outcome (a forged/zero
// value, whose String() renders as the *Unknown sentinel) is NEVER recorded — so
// the closed sets cannot be polluted. The sealed types (no external value) + this
// record-point guard + the allTransportModes/allTransportOutcomes registries
// triple-close both label value sets.
func (m *Metrics) Record(ctx context.Context, mode TransportMode, outcome TransportOutcome) {
	if m == nil {
		return
	}
	if !mode.isRegistered() || !outcome.isRegistered() {
		return
	}
	m.requests.With(kernelmetrics.Labels{
		labelTransportMode: mode.String(),
		labelOutcome:       outcome.String(),
	}).Inc(ctx)
}
