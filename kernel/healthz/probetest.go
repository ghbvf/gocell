package healthz

import "context"

// NewTestOnlyProbeWithAnyName constructs a [Probe] that bypasses the empty-name
// guard enforced by [NewProbe]. It is intended ONLY for conformance-test harnesses
// that need to exercise Aggregator validation of malformed probes (e.g. a probe
// whose Name() returns the empty string, to verify ErrInvalidProbeName is
// returned).
//
// Production code must NOT call this function; use [NewProbe] instead.
// Caller allowlist enforced by archtest PROBENAME-SEALED-FUNNEL-01/A7
// (testOnlyProbeAllowlist) — currently the only sanctioned non-_test.go
// caller is runtime/observability/healthz/healthztest/conformance.go. Any
// new non-test caller must add itself to the allowlist + justify in PR
// review (Medium upstream archtest; Go ceiling — exported test helpers in
// kernel/ packages have no sealed-construction path).
func NewTestOnlyProbeWithAnyName(name ProbeName, fn func(context.Context) error) Probe {
	return funcProbe{name: name, fn: fn}
}
