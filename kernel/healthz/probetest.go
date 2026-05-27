package healthz

import "context"

// NewTestOnlyProbeWithAnyName constructs a [Probe] that bypasses the empty-name
// guard enforced by [NewProbe]. It is intended ONLY for conformance-test harnesses
// that need to exercise Aggregator validation of malformed probes (e.g. a probe
// whose Name() returns the empty string, to verify ErrInvalidProbeName is
// returned).
//
// Production code must NOT call this function; use [NewProbe] instead.
// Archtest PROBENAME-SEALED-FUNNEL-01 does not scan this function.
func NewTestOnlyProbeWithAnyName(name ProbeName, fn func(context.Context) error) Probe {
	return funcProbe{name: name, fn: fn}
}
