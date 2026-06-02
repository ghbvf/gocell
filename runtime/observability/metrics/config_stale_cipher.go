package metrics

import (
	"context"
	"fmt"

	kernelmetrics "github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ConfigStaleCipherCollector records reads of config values that are encrypted
// with a non-current key version (M3 stale-key observability).
type ConfigStaleCipherCollector interface {
	RecordStaleCipher(ctx context.Context)
}

// NoopConfigStaleCipherCollector drops stale-cipher observations.
type NoopConfigStaleCipherCollector struct{}

func (NoopConfigStaleCipherCollector) RecordStaleCipher(_ context.Context) {
	// Intentionally empty: callers can inject this collector when stale-cipher
	// metrics are disabled while keeping service code free of nil checks.
}

type providerConfigStaleCipherCollector struct {
	staleCipher kernelmetrics.CounterVec
}

var _ ConfigStaleCipherCollector = (*providerConfigStaleCipherCollector)(nil)

// NewProviderConfigStaleCipherCollector registers the config stale-cipher counter
// on p. The Prometheus provider namespace supplies the "gocell_" fqName prefix,
// so Name "config_stale_cipher_total" yields the wire metric
// "gocell_config_stale_cipher_total" — byte-identical to the prior raw-prometheus
// counter that lived in cmd/corebundle (configStaleCipherOpts). The metric is
// unlabeled (a single global count), so RecordStaleCipher increments the one
// series via the empty label set.
func NewProviderConfigStaleCipherCollector(p kernelmetrics.Provider) (ConfigStaleCipherCollector, error) {
	if p == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrObservabilityConfigInvalid,
			"runtime/observability/metrics: config stale-cipher Provider is required")
	}
	staleCipher, err := p.CounterVec(kernelmetrics.CounterOpts{
		Name: "config_stale_cipher_total",
		Help: "Number of config values read that are encrypted with a non-current key version.",
	})
	if err != nil {
		return nil, fmt.Errorf("runtime/observability/metrics: register config_stale_cipher_total: %w", err)
	}
	return &providerConfigStaleCipherCollector{staleCipher: staleCipher}, nil
}

func (c *providerConfigStaleCipherCollector) RecordStaleCipher(ctx context.Context) {
	if c == nil {
		return
	}
	c.staleCipher.With(kernelmetrics.Labels{}).Inc(ctx)
}
