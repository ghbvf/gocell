package metrics

import (
	"fmt"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
)

// registerCounterVec registers one counter and appends it to *registered on
// success. On failure it tears down every previously-registered counter LIFO
// (#1181 F11 atomic registration: the provider registry must not retain orphans
// so a retry can re-register under the same names — mirrors
// prometheus/client_golang Registry.Unregister) and wraps the error with the
// metric name. Shared by SagaCollector (saga.go) and SessionCacheCollector
// (session_cache.go).
func registerCounterVec(
	p kernelmetrics.Provider, opts kernelmetrics.CounterOpts, registered *[]kernelmetrics.Collector,
) (kernelmetrics.CounterVec, error) {
	cv, err := p.CounterVec(opts)
	if err != nil {
		for i := len(*registered) - 1; i >= 0; i-- {
			_ = p.Unregister((*registered)[i])
		}
		return nil, fmt.Errorf("runtime/observability/metrics: register %s: %w", opts.Name, err)
	}
	*registered = append(*registered, cv)
	return cv, nil
}

// registerGaugeVec registers one gauge and appends it to *registered on success.
// On failure it tears down every previously-registered collector LIFO (same
// atomic-registration discipline as registerCounterVec) and wraps the error with
// the metric name.
func registerGaugeVec(
	p kernelmetrics.Provider, opts kernelmetrics.GaugeOpts, registered *[]kernelmetrics.Collector,
) (kernelmetrics.GaugeVec, error) {
	gv, err := p.GaugeVec(opts)
	if err != nil {
		for i := len(*registered) - 1; i >= 0; i-- {
			_ = p.Unregister((*registered)[i])
		}
		return nil, fmt.Errorf("runtime/observability/metrics: register %s: %w", opts.Name, err)
	}
	*registered = append(*registered, gv)
	return gv, nil
}
