// Package metrics defines a provider-neutral metrics abstraction used by
// kernel modules that emit counters and histograms without importing any
// specific backend (Prometheus, OTel, …). Concrete providers live in
// adapters/; runtime/ modules pick one and inject it via configuration.
//
// ref: opentelemetry-go metric/meter.go@main — API/SDK split pattern.
// ref: prometheus/client_golang prometheus/counter.go@main CounterVec.With() —
// pre-declared label names bound at record time. GoCell uses the Prom shape
// (LabelNames at registration, Labels map at record) over OTel's variadic
// attribute.KeyValue because a map makes callers name their dimensions and
// makes label-set drift a detectable error rather than a silent mismatch.
//
// Counter/Histogram/Gauge 方法都接受 context.Context 首参，对齐 OTel 原生
// ctx-bearing 形态：OTel adapter 把 ctx 转发给底层 instrument 以支持 exemplar /
// baggage 关联（需传请求级 ctx，含 active span 才有意义）；Prometheus adapter 不
// 消费 ctx。约定两条：(1) 实现禁止因 ctx 已取消/超时而跳过记录——指标发射是
// best-effort，ctx 取消不得导致数据丢失；(2) 无请求 ctx 的后台路径（如 hook
// dispatcher worker）显式传 context.Background() 并就近注释说明。
package metrics

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/panicregister"
)

// Collector is a handle to a registered metric family (counter, histogram, or
// gauge vec). It is returned by CounterVec/HistogramVec/GaugeVec and used only
// through the typed vec interfaces below.
//
// ref: prometheus/client_golang prometheus/collector.go — Collector is the
// registration unit. GoCell's Collector is a thinner typed handle that keeps
// kernel code free of Prometheus imports.
type Collector interface {
	// Registered is a compile-time type-membership marker, not a runtime
	// state probe. It always returns true for vecs returned by a Provider;
	// Collector does not expose registry lifecycle; provider shutdown or
	// replacement is owned by the concrete backend wiring.
	//
	// All concrete vec types (prom, otel, nop, test spy) implement this
	// method; external code must not implement Collector directly.
	Registered() bool
}

// Provider registers metric instruments. Implementations are provided by
// adapters/ (prometheus, otel). Kernel code accepts a Provider interface
// value; at wire time (runtime/bootstrap, cmd/*), a concrete backend is
// chosen and passed through.
//
// Registration is failable (duplicate names, invalid options) so factory
// methods return (vec, error). Callers are expected to register at start-up and
// treat errors as fatal. Provider is intentionally create-only: lifecycle is
// owned at the concrete backend boundary (e.g. Prometheus Registry or OTel
// MeterProvider), not by individual metric instruments.
type Provider interface {
	CounterVec(opts CounterOpts) (CounterVec, error)
	HistogramVec(opts HistogramOpts) (HistogramVec, error)
	// GaugeVec registers a gauge metric family. Gauges are arbitrary-valued
	// instruments that can move up or down (Set / Inc / Dec / Add). Typical
	// uses: queue depth, active workers, in-flight requests — point-in-time
	// snapshots where Counter monotonicity is unsuitable.
	//
	// ref: prometheus/client_golang prometheus/gauge.go — Gauge interface
	// methods (Set / Inc / Dec / Add) adopted; Sub omitted (use Add(-delta));
	// SetToCurrentTime omitted (no business need; introduces implicit clock
	// dependency at kernel layer).
	GaugeVec(opts GaugeOpts) (GaugeVec, error)
}

// CounterOpts declares a counter metric family.
type CounterOpts struct {
	Name       string
	Help       string
	LabelNames []string // Order-sensitive; used by adapters to compose the underlying vec.
}

// HistogramOpts declares a histogram metric family.
type HistogramOpts struct {
	Name       string
	Help       string
	LabelNames []string
	// Buckets lists upper bounds in seconds (or whatever unit the histogram
	// records). Empty slice means "use adapter default". Callers should
	// supply explicit buckets for any metric that leaves kernel, to keep
	// cardinality predictable across backends.
	Buckets []float64
}

// GaugeOpts declares a gauge metric family. Shape mirrors CounterOpts —
// Name + Help + LabelNames. No bucket field (gauges are not aggregated
// histograms).
//
// High-cardinality warning: every unique label tuple creates a separate
// time series held in the Provider's registry; for dimensions with
// unbounded value spaces (request id, user id), prefer pre-currying with
// fixed dimensions or use a different metric type.
type GaugeOpts struct {
	Name       string
	Help       string
	LabelNames []string // Order-sensitive; used by adapters to compose the underlying vec.
}

// Labels carries label values at record time. Keys MUST exactly match the
// LabelNames declared at registration. With() panics on mismatch because
// label drift is a programmer bug, not a runtime condition, and a silent
// mismatch would produce misattributed or dropped data points that are
// extremely hard to debug in production.
type Labels map[string]string

// CounterVec returns a pre-bound Counter given a label set. Implementations
// panic (via MustValidateLabels) when Labels does not exactly match the
// LabelNames set at registration. It embeds Collector so all vecs share the
// provider-owned lifecycle marker.
type CounterVec interface {
	Collector
	With(Labels) Counter
}

// HistogramVec returns a pre-bound Histogram given a label set. It embeds
// Collector so all vecs share the provider-owned lifecycle marker.
type HistogramVec interface {
	Collector
	With(Labels) Histogram
}

// GaugeVec returns a pre-bound Gauge given a label set. Implementations
// panic (via MustValidateLabels) when Labels does not exactly match the
// LabelNames set at registration. It embeds Collector so all vecs share the
// provider-owned lifecycle marker.
type GaugeVec interface {
	Collector
	With(Labels) Gauge
}

// Counter is a monotonically increasing counter, pre-bound to a label set.
type Counter interface {
	Inc(ctx context.Context)
	Add(ctx context.Context, delta float64)
}

// Histogram records observations into predeclared buckets, pre-bound to a
// label set.
type Histogram interface {
	Observe(ctx context.Context, value float64)
}

// Gauge is an arbitrary-valued instrument that can move up or down,
// pre-bound to a label set. Use Set for point-in-time snapshots
// (queue depth, active workers); Inc/Dec/Add for delta updates.
//
// Sub is deliberately omitted: Add(-delta) expresses the same semantics
// and keeps the method set symmetric with Counter.
//
// ref: prometheus/client_golang prometheus/gauge.go — Gauge interface.
type Gauge interface {
	Set(ctx context.Context, value float64)
	Inc(ctx context.Context)
	Dec(ctx context.Context)
	Add(ctx context.Context, delta float64)
}

// ErrLabelMismatch is returned / panic-wrapped by ValidateLabels /
// MustValidateLabels when the supplied Labels do not exactly cover the
// registered LabelNames. Callers can errors.Is against this sentinel when
// converting label-validation errors into structured diagnostics.
var ErrLabelMismatch = errcode.New(errcode.KindInvalid, errcode.ErrMetricsLabelMismatch,
	"metrics: label keys do not match registered LabelNames")

// ErrLabelValueIllegal is returned when a label value contains a separator
// reserved by the OTel-provider cache key (`|` or `=`). A collision here
// causes silently-misattributed data points — we prefer a panic at
// registration time over a wrong-but-present time-series in production.
var ErrLabelValueIllegal = errcode.New(errcode.KindInvalid, errcode.ErrMetricsLabelValueIllegal,
	"metrics: label value contains reserved separator")

// labelSeparators are characters reserved for the label cache key
// encoding used by the OTel adapter (adapters/otel/metric_provider.go).
// Reserving them in the kernel keeps value-encoding rules in one place,
// so an adapter change cannot silently diverge from the contract.
const labelSeparators = "|="

// ValidateLabels returns a descriptive error when labels do not exactly
// cover expected. It compares as sets: any missing, extra, or wrong key
// is an error. Both nil or empty inputs are considered a match. Values
// containing characters from labelSeparators (`|` or `=`) are rejected
// because the OTel adapter's per-label-set cache keys them positionally;
// a value with a separator would collide silently.
func ValidateLabels(expected []string, got Labels) error {
	if len(got) != len(expected) {
		return fmt.Errorf("%w: want %d keys %v, got %d %v",
			ErrLabelMismatch, len(expected), expected, len(got), sortedKeys(got))
	}
	for _, k := range expected {
		v, ok := got[k]
		if !ok {
			return fmt.Errorf("%w: missing key %q (expected %v, got %v)",
				ErrLabelMismatch, k, expected, sortedKeys(got))
		}
		if strings.ContainsAny(v, labelSeparators) {
			return fmt.Errorf("%w: value for key %q is %q (separators %q reserved by adapter cache)",
				ErrLabelValueIllegal, k, v, labelSeparators)
		}
	}
	return nil
}

// MustValidateLabels panics with a wrapped ErrLabelMismatch when labels do
// not match. Adapter With() implementations call this as the first line so
// a programmer bug surfaces immediately with a precise message. Uses
// errcode.Wrap (not errcode.Assertion) so consumers can errors.Is(panic,
// metrics.ErrLabelMismatch) through the *errcode.Error.Cause chain.
func MustValidateLabels(expected []string, got Labels) {
	if err := ValidateLabels(expected, got); err != nil {
		panic(panicregister.Approved("metrics-validate-labels-mismatch",
			errcode.Wrap(errcode.KindInternal, errcode.ErrInternal,
				"metrics: invalid labels", err,
				errcode.WithCategory(errcode.CategoryInfra))))
	}
}

// sortedKeys returns the map keys in lexical order for deterministic error
// messages. Allocates on every call; only used on the error path.
func sortedKeys(m Labels) []string {
	if len(m) == 0 {
		return nil
	}
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
