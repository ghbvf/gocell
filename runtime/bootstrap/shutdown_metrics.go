package bootstrap

import (
	"fmt"
	"time"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
)

// Metric names for shutdown observability. All names carry the gocell_ prefix
// per the project naming convention (ref: kernel/outbox relay_collector.go).
const (
	// shutdownPhaseCounterName counts entries into each named shutdown phase.
	// Labels: phase = readiness_flip | lifo_teardown | closed.
	// SRE use: detect stuck shutdowns by comparing phase entry counts across
	// instances; a missing "closed" entry pinpoints where the hang occurred.
	//
	// Note: This is a Counter (not a Gauge). To check which phase a pod is stuck
	// in, operators must compare the delta between successive phase counters; a
	// phase counter that fails to increment while earlier phases have incremented
	// indicates the pod is stuck in the previous phase.
	shutdownPhaseCounterName = "gocell_bootstrap_shutdown_phase_entries_total"

	// shutdownPhaseDurationName records per-phase wall-clock latency.
	// Labels: phase = readiness_flip | lifo_teardown | total.
	// SRE use: P99 histogram in Grafana reveals which phase dominates
	// shutdown latency.
	shutdownPhaseDurationName = "gocell_bootstrap_shutdown_phase_duration_seconds"

	// shutdownTotalCounterName counts completed shutdowns by outcome.
	// Labels: outcome ∈ {success, timeout, teardown_error, signal_error}.
	//   - success       : clean, user-initiated shutdown
	//   - timeout       : shutCtx expired during readiness flip or LIFO teardown
	//   - teardown_error: at least one teardown returned non-nil (no timeout)
	//   - signal_error  : shutdown triggered by HTTP/worker/router failure,
	//                     teardown itself was clean
	// SRE use: alert on timeout / teardown_error; signal_error rate reveals
	// how often shutdown is triggered by component failures vs. human action.
	// ref: Kubernetes pod termination (success/failure/timeout tri-state).
	shutdownTotalCounterName = "gocell_bootstrap_shutdown_total"
)

// Phase label values for shutdownPhaseCounterName.
const (
	shutdownPhaseReadinessFlip = "readiness_flip"
	shutdownPhaseLIFOTeardown  = "lifo_teardown"
	shutdownPhaseClosed        = "closed"
)

// registerErrFmt is the error-wrap format shared by every metric registration
// failure in newShutdownMetrics — keeping a single literal simplifies log
// parsing and avoids drift between the three call sites.
const registerErrFmt = "bootstrap: register %s: %w"

// defaultShutdownBuckets are histogram upper bounds in seconds for per-phase
// shutdown duration. Range covers 10ms (fast path) to 60s (termination grace
// period). ref: kernel/outbox DefaultRelayPollBuckets — same bucketing philosophy.
var defaultShutdownBuckets = []float64{0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

// shutdownMetrics groups the three observability signals emitted during
// phase10OrchestrateShutdown. A nil *shutdownMetrics is safe to call —
// all methods are nil-receiver guards. This enables the "disabled without
// provider" contract without nil checks at every call site.
//
// Design decision (plan option B): shutdownMetrics is created once in New()
// and stored on Bootstrap so metric instruments are registered against the
// Provider at construction time, matching the "register at start-up" pattern
// used by relay_collector.go and the kernel hook dispatcher.
type shutdownMetrics struct {
	// phaseEntries counts each phase transition. Using a CounterVec (not a
	// Gauge) because the kernel Provider interface does not expose Gauge.
	// A counter-per-phase lets SREs detect missing phase entries (stuck
	// shutdown) and build timeline views by comparing instance counts.
	// This is a pragmatic adaptation: the plan requested a "gauge" but the
	// kernel abstraction has no Gauge primitive. A single-label CounterVec
	// encodes the same information for the SRE use cases described in the
	// task spec.
	phaseEntries kernelmetrics.CounterVec

	// phaseDuration records wall-clock seconds for readiness_flip,
	// lifo_teardown, and the overall total.
	phaseDuration kernelmetrics.HistogramVec

	// shutdownTotal counts completed shutdowns by outcome (success | timeout).
	shutdownTotal kernelmetrics.CounterVec
}

// newShutdownMetrics registers shutdown metrics on p and returns a
// *shutdownMetrics. Returns (nil, nil) when p is nil, which disables
// all metric emission while leaving phase10 behaviour unchanged.
//
// An error is returned only when the Provider itself fails to register a
// metric family (typically a duplicate name in the same registry). Callers
// treat this as fatal (consistent with relay_collector.go registration pattern).
//
// ref: kernel/outbox.NewProviderRelayCollector — rollback-on-partial-failure
// pattern for metric registration.
func newShutdownMetrics(p kernelmetrics.Provider) (*shutdownMetrics, error) {
	if p == nil {
		return nil, nil
	}

	// Track registered collectors for rollback on partial failure.
	var registered []kernelmetrics.Collector
	rollback := func(origErr error) (*shutdownMetrics, error) {
		for i := len(registered) - 1; i >= 0; i-- {
			_ = p.Unregister(registered[i]) // best-effort; ignore unregister errors
		}
		return nil, origErr
	}

	phaseEntries, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name:       shutdownPhaseCounterName,
		Help:       "Total entries into each shutdown phase.",
		LabelNames: []string{"phase"},
	})
	if err != nil {
		return rollback(fmt.Errorf(registerErrFmt, shutdownPhaseCounterName, err))
	}
	registered = append(registered, phaseEntries)

	phaseDuration, err := p.HistogramVec(kernelmetrics.HistogramOpts{
		Name:       shutdownPhaseDurationName,
		Help:       "Wall-clock duration of each shutdown phase in seconds.",
		LabelNames: []string{"phase"},
		Buckets:    defaultShutdownBuckets,
	})
	if err != nil {
		return rollback(fmt.Errorf(registerErrFmt, shutdownPhaseDurationName, err))
	}
	registered = append(registered, phaseDuration)

	shutdownTotal, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name:       shutdownTotalCounterName,
		Help:       "Total completed shutdowns by outcome (success|timeout).",
		LabelNames: []string{"outcome"},
	})
	if err != nil {
		return rollback(fmt.Errorf(registerErrFmt, shutdownTotalCounterName, err))
	}

	return &shutdownMetrics{
		phaseEntries:  phaseEntries,
		phaseDuration: phaseDuration,
		shutdownTotal: shutdownTotal,
	}, nil
}

// recordPhaseEntry increments the phase-entry counter for the given phase
// label. No-op on nil receiver.
func (m *shutdownMetrics) recordPhaseEntry(phase string) {
	if m == nil {
		return
	}
	m.phaseEntries.With(kernelmetrics.Labels{"phase": phase}).Inc()
}

// observePhaseDuration records the duration of a shutdown phase. No-op on
// nil receiver.
func (m *shutdownMetrics) observePhaseDuration(phase string, d time.Duration) {
	if m == nil {
		return
	}
	m.phaseDuration.With(kernelmetrics.Labels{"phase": phase}).Observe(d.Seconds())
}

// countOutcome increments the shutdown outcome counter. outcome must be
// "success" or "timeout". No-op on nil receiver.
func (m *shutdownMetrics) countOutcome(outcome string) {
	if m == nil {
		return
	}
	m.shutdownTotal.With(kernelmetrics.Labels{"outcome": outcome}).Inc()
}
