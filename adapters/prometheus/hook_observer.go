package prometheus

import (
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/pkg/errcode"
	prom "github.com/prometheus/client_golang/prometheus"
)

// DefaultHookDurationBuckets covers the range expected for cell lifecycle
// hooks: from sub-millisecond in-process init up to the 30s default
// HookTimeout so timeout-adjacent latency remains observable.
var DefaultHookDurationBuckets = []float64{
	.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30,
}

// HookObserverConfig configures the Prometheus implementation of
// cell.LifecycleHookObserver.
type HookObserverConfig struct {
	// Registry is the Prometheus registry to use. Must be non-nil; the caller
	// owns the lifecycle so multiple observers (hook + HTTP + adapter pools)
	// can share one exposition endpoint.
	Registry *prom.Registry

	// Namespace prefixes metric names. Default: "gocell".
	Namespace string

	// DurationBuckets configures the histogram bucket boundaries in seconds.
	// Default: DefaultHookDurationBuckets.
	DurationBuckets []float64
}

func (c *HookObserverConfig) defaults() {
	if c.Namespace == "" {
		c.Namespace = "gocell"
	}
	if len(c.DurationBuckets) == 0 {
		c.DurationBuckets = DefaultHookDurationBuckets
	}
}

// HookObserver implements cell.LifecycleHookObserver by emitting Prometheus
// metrics on every hook event.
//
// Metrics:
//
//	{namespace}_cell_hook_total{cell_id,hook,outcome}         — counter
//	{namespace}_cell_hook_duration_seconds{cell_id,hook}      — histogram
//
// Cardinality budget: (number of cells) × 4 hooks × 4 outcomes = small.
// Assumes static cell registration at assembly build time — dynamic or
// per-tenant cells (e.g. a cell-per-customer topology) would require
// cardinality budgeting and likely a label aggregation strategy.
// Duration histogram intentionally omits `outcome` label to keep bucket
// count bounded — failures and successes share the same time distribution
// bucket set.
//
// ref: uber-go/fx fxevent/logger.go@master — single-method observer pattern.
// ref: adapters/prometheus/collector.go — CounterVec/HistogramVec style.
type HookObserver struct {
	hookTotal    *prom.CounterVec
	hookDuration *prom.HistogramVec
}

// Compile-time interface check.
var _ cell.LifecycleHookObserver = (*HookObserver)(nil)

// NewHookObserver constructs a HookObserver and registers its metrics on
// cfg.Registry. Returns an error if registration fails (e.g., duplicate
// registration on the same registry).
func NewHookObserver(cfg HookObserverConfig) (*HookObserver, error) {
	if cfg.Registry == nil {
		return nil, errcode.New(ErrAdapterPromConfig, "prometheus hook observer: Registry is required")
	}
	cfg.defaults()

	hookTotal := prom.NewCounterVec(prom.CounterOpts{
		Namespace: cfg.Namespace,
		Name:      "cell_hook_total",
		Help:      "Total number of cell lifecycle hook invocations, partitioned by outcome.",
	}, []string{"cell_id", "hook", "outcome"})

	hookDuration := prom.NewHistogramVec(prom.HistogramOpts{
		Namespace: cfg.Namespace,
		Name:      "cell_hook_duration_seconds",
		Help:      "Duration of cell lifecycle hook invocations in seconds.",
		Buckets:   cfg.DurationBuckets,
	}, []string{"cell_id", "hook"})

	if err := cfg.Registry.Register(hookTotal); err != nil {
		return nil, errcode.Wrap(ErrAdapterPromRegister,
			"prometheus hook observer: register cell_hook_total", err)
	}
	if err := cfg.Registry.Register(hookDuration); err != nil {
		// Rollback the first collector to keep construction atomic — otherwise
		// a retry with the same registry would fail with "already registered"
		// on hookTotal and leak a dangling half-registered observer.
		cfg.Registry.Unregister(hookTotal)
		return nil, errcode.Wrap(ErrAdapterPromRegister,
			"prometheus hook observer: register cell_hook_duration_seconds", err)
	}

	return &HookObserver{hookTotal: hookTotal, hookDuration: hookDuration}, nil
}

// OnHookEvent records the event. Safe for concurrent use (Prometheus vec
// types are goroutine-safe).
func (o *HookObserver) OnHookEvent(e cell.HookEvent) {
	o.hookTotal.WithLabelValues(e.CellID, string(e.Hook), string(e.Outcome)).Inc()
	o.hookDuration.WithLabelValues(e.CellID, string(e.Hook)).Observe(e.Duration.Seconds())
}
