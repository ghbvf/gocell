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
package metrics

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Collector is a handle to a registered metric family (counter or histogram
// vec). It is returned by CounterVec/HistogramVec and accepted by Unregister.
//
// Callers obtain Collector values only via Provider.CounterVec and
// Provider.HistogramVec; passing other values to Unregister is undefined
// behaviour (implementations may silently no-op or return an error).
//
// Both CounterVec and HistogramVec embed Collector so that the return values
// of CounterVec/HistogramVec can be passed directly to Unregister without
// explicit type assertions.
//
// ref: prometheus/client_golang prometheus/collector.go — Collector is the
// registration unit. GoCell's Collector is a thinner typed handle that keeps
// kernel code free of Prometheus imports.
type Collector interface {
	// Registered is a compile-time type-membership marker, not a runtime
	// state probe. It always returns true for vecs returned by a Provider;
	// after Unregister, implementations may still return true because the
	// collector value itself remains valid — the change is only in the
	// Provider's registry state, not the vec's identity.
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
// Registration is failable (duplicate names, invalid options) so both
// factory methods return (vec, error). Callers are expected to register
// at start-up and treat errors as fatal.
type Provider interface {
	CounterVec(opts CounterOpts) (CounterVec, error)
	HistogramVec(opts HistogramOpts) (HistogramVec, error)
	// Unregister removes a previously registered collector from the provider's
	// registry. It is safe for concurrent use and idempotent — unregistering a
	// collector that was never registered (or already unregistered) returns nil
	// without error.
	//
	// Implementations must maintain the invariant that a collector successfully
	// Unregistered can be re-registered via CounterVec/HistogramVec under the
	// same name without conflict.
	//
	// ref: prometheus/client_golang Registry.Unregister — bool return simplified
	// to error for GoCell consistency (nil = success or not-found; non-nil =
	// hard failure).
	Unregister(c Collector) error
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

// Labels carries label values at record time. Keys MUST exactly match the
// LabelNames declared at registration. With() panics on mismatch because
// label drift is a programmer bug, not a runtime condition, and a silent
// mismatch would produce misattributed or dropped data points that are
// extremely hard to debug in production.
type Labels map[string]string

// CounterVec returns a pre-bound Counter given a label set. Implementations
// panic (via MustValidateLabels) when Labels does not exactly match the
// LabelNames set at registration.
//
// CounterVec embeds Collector so that callers can pass it directly to
// Provider.Unregister without an explicit type cast.
type CounterVec interface {
	Collector
	With(Labels) Counter
}

// HistogramVec returns a pre-bound Histogram given a label set.
//
// HistogramVec embeds Collector so that callers can pass it directly to
// Provider.Unregister without an explicit type cast.
type HistogramVec interface {
	Collector
	With(Labels) Histogram
}

// Counter is a monotonically increasing counter, pre-bound to a label set.
type Counter interface {
	Inc()
	Add(delta float64)
}

// Histogram records observations into predeclared buckets, pre-bound to a
// label set.
type Histogram interface {
	Observe(value float64)
}

// ErrLabelMismatch is returned / panic-wrapped by ValidateLabels /
// MustValidateLabels when the supplied Labels do not exactly cover the
// registered LabelNames. Callers can errors.Is against this sentinel when
// converting label-validation errors into structured diagnostics.
var ErrLabelMismatch = errors.New("metrics: label keys do not match registered LabelNames")

// ErrLabelValueIllegal is returned when a label value contains a separator
// reserved by the OTel-provider cache key (`|` or `=`). A collision here
// causes silently-misattributed data points — we prefer a panic at
// registration time over a wrong-but-present time-series in production.
var ErrLabelValueIllegal = errors.New("metrics: label value contains reserved separator")

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
// a programmer bug surfaces immediately with a precise message.
func MustValidateLabels(expected []string, got Labels) {
	if err := ValidateLabels(expected, got); err != nil {
		panic(err)
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
