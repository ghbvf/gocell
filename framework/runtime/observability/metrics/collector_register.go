package metrics

import (
	"fmt"

	kernelmetrics "github.com/ghbvf/gocell/framework/kernel/observability/metrics"
)

// registerCounterVec registers one counter and wraps provider errors with the
// metric name. Metrics registration is a startup construction step: callers
// treat failures as fatal for the current wiring and do not rely on
// per-instrument rollback across backends.
func registerCounterVec(
	p kernelmetrics.Provider, opts kernelmetrics.CounterOpts,
) (kernelmetrics.CounterVec, error) {
	cv, err := p.CounterVec(opts)
	if err != nil {
		return nil, fmt.Errorf("runtime/observability/metrics: register %s: %w", opts.Name, err)
	}
	return cv, nil
}

// registerGaugeVec registers one gauge and wraps provider errors with the
// metric name. See registerCounterVec for the startup-fatal lifecycle contract.
func registerGaugeVec(
	p kernelmetrics.Provider, opts kernelmetrics.GaugeOpts,
) (kernelmetrics.GaugeVec, error) {
	gv, err := p.GaugeVec(opts)
	if err != nil {
		return nil, fmt.Errorf("runtime/observability/metrics: register %s: %w", opts.Name, err)
	}
	return gv, nil
}
