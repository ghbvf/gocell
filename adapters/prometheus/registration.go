package prometheus

// File-level: Public adapter-level passthrough constructors. They exist only
// for the bare/single-instrument and callback-style (Func) constructors that
// adapter-external callers (e.g. adapters/vault) need but cannot reach
// directly because internal/promwrap is Go-internal.
//
// Vec constructors (CounterVec, GaugeVec, HistogramVec) intentionally do NOT
// have peers here — they are exposed via MetricProvider.{CounterVec,GaugeVec,
// HistogramVec} which routes through internal/promwrap, registers with the
// adapter's Prometheus Registry, and applies the kernel metrics.Provider
// validation contract (MustValidateLabels, attrCache cardinality protection).
// External callers needing labeled metrics should depend on the kernel
// metrics.Provider abstraction; only bare/Func variants justify a direct
// adapter passthrough.
//
// NewCounterVec is the ONE exception, and it is debt — not a sanctioned
// labeled-metric path. It exists solely because adapters/vault's loginOutcome
// CounterVec predates the kernel metrics.Provider and cannot use it yet: vault
// hands unregistered collectors to the composition root to register on a
// dedicated registry, whereas metrics.Provider registers internally and returns
// Provider-managed handles. This labeled escape hatch bypasses the Provider's
// label/cardinality governance and is scheduled for removal (vault migrates
// loginOutcome to metrics.Provider.CounterVec) — tracked by issue #885. Do NOT
// add new callers; new labeled metrics MUST go through metrics.Provider.

import (
	"errors"
	"fmt"

	prom "github.com/prometheus/client_golang/prometheus"

	"github.com/ghbvf/gocell/adapters/prometheus/internal/promwrap"
)

// RegisterOrReuseCounter registers a new counter on the given Registerer with
// the given opts. If the counter is already registered (AlreadyRegisteredError),
// it reuses the existing collector. Non-AlreadyRegisteredError failures are
// returned unwrapped. If the registered collector is not a prom.Counter, an
// error is returned wrapping the original AlreadyRegisteredError with the prefix
// "existing collector is not a Counter:".
//
// This is the only sanctioned entry point for creating a bare prom.Counter
// outside the metrics.Provider abstraction — adapter-internal idempotent
// register-or-reuse pattern, originally lifted from cmd/corebundle.
//
// Note: as of #1413 there is no production caller — configcore's stale-cipher
// counter (its sole consumer) migrated to the kernel MetricsProvider. The export
// is retained as the sanctioned bare-counter helper for future adapter-internal
// use; its caller-allowlist (METRICS-ADAPTERPROM-CALLER-ALLOWLIST-01) is empty, so
// a new caller must add its file there with justification.
//
// ref: kubernetes component-base/metrics/counter.go (idempotent registration pattern)
func RegisterOrReuseCounter(reg prom.Registerer, opts prom.CounterOpts) (prom.Counter, error) {
	c := promwrap.NewCounter(opts)
	if err := reg.Register(c); err != nil {
		var are prom.AlreadyRegisteredError
		if !errors.As(err, &are) {
			return nil, err
		}
		if existing, ok := are.ExistingCollector.(prom.Counter); ok {
			return existing, nil
		}
		return nil, fmt.Errorf("existing collector is not a Counter: %w", err)
	}
	return c, nil
}

// NewCounter is the public adapter-level entry point for creating a bare
// prom.Counter without registering it. Adapter packages outside the
// prometheus subtree (e.g. adapters/vault) that cannot import
// adapters/prometheus/internal/promwrap directly due to Go internal/ closure
// must use this function instead of calling prom.NewCounter directly.
//
// Does NOT register the collector with any Registerer; callers must Register
// it themselves to expose it in /metrics.
//
// Callers that also need to register the counter should use RegisterOrReuseCounter.
func NewCounter(opts prom.CounterOpts) prom.Counter {
	return promwrap.NewCounter(opts)
}

// NewCounterVec is a vault-only legacy carve-out, NOT a general labeled-metric
// entry point — see the file-level doc and issue #885. It returns an
// unregistered *prom.CounterVec for adapters/vault's loginOutcome, which still
// uses the register-at-composition-root model and so cannot route through
// metrics.Provider.CounterVec yet. It deliberately bypasses the Provider's
// label/cardinality governance; new labeled metrics MUST use metrics.Provider
// instead. Callers must Register the returned collector themselves.
//
// Pending removal: vault migrates loginOutcome to metrics.Provider.CounterVec
// (issue #885), after which this function is deleted.
func NewCounterVec(opts prom.CounterOpts, labelNames []string) *prom.CounterVec {
	return promwrap.NewCounterVec(opts, labelNames)
}

// NewGauge is the public adapter-level entry point for creating a bare
// prom.Gauge without registering it. Adapter packages outside the prometheus
// subtree (e.g. adapters/vault) that cannot import
// adapters/prometheus/internal/promwrap directly due to Go internal/ closure
// must use this function instead of calling prom.NewGauge directly.
//
// Does NOT register the collector with any Registerer; callers must Register
// it themselves to expose it in /metrics.
func NewGauge(opts prom.GaugeOpts) prom.Gauge {
	return promwrap.NewGauge(opts)
}

// NewGaugeFunc is the public adapter-level entry point for creating a
// callback-style prom.GaugeFunc without registering it. Adapter packages
// outside the prometheus subtree (e.g. adapters/vault) that cannot import
// adapters/prometheus/internal/promwrap directly due to Go internal/ closure
// must use this function instead of calling prom.NewGaugeFunc directly.
//
// Does NOT register the collector with any Registerer; callers must Register
// it themselves to expose it in /metrics.
func NewGaugeFunc(opts prom.GaugeOpts, function func() float64) prom.GaugeFunc {
	return promwrap.NewGaugeFunc(opts, function)
}
