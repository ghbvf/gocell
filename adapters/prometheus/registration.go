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
// adapter passthrough. (NewCounterVec lives here as the lone exception for
// adapters/vault's renewal-attempt counter, which uses prom.Registerer
// directly without the kernel Provider's idempotent-register-or-reuse path.)

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

// NewCounterVec is the public adapter-level entry point for creating a bare
// prom.CounterVec without registering it. Adapter packages outside the
// prometheus subtree (e.g. adapters/vault) that cannot import
// adapters/prometheus/internal/promwrap directly due to Go internal/ closure
// must use this function instead of calling prom.NewCounterVec directly.
//
// Does NOT register the collector with any Registerer; callers must Register
// it themselves to expose it in /metrics.
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
