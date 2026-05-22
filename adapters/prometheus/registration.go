package prometheus

import (
	"errors"
	"fmt"

	prom "github.com/prometheus/client_golang/prometheus"

	"github.com/ghbvf/gocell/adapters/prometheus/internal/promwrap"
)

// RegisterOrReuseCounter registers a new counter on the given Registerer with
// the given opts. If the counter is already registered (AlreadyRegisteredError),
// it reuses the existing collector. Any other registration error is returned
// as-is.
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
