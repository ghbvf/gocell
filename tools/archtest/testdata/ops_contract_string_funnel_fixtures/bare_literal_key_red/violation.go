// Package bare_literal_key_red is a testdata fixture for
// OPS-CONTRACT-STRING-FUNNEL-01 blind spot #1: a Checkers() map[string]func
// literal whose keys are a bare string literal and a string concat must both
// be flagged by scanReadyProbeConstructionViolations, while the string(<const>)
// key passes. Uses a local ReadyProbeName mirror type to isolate the
// construction-scan logic (the production package filter is exercised
// separately by the production load).
package bare_literal_key_red

import "context"

// ReadyProbeName mirrors kernel/healthz.ReadyProbeName.
type ReadyProbeName string

// ProbeReady is the single sanctioned probe-name const for this fixture.
const ProbeReady ReadyProbeName = "ok_ready"

// Res implements a Checkers()-shaped method.
type Res struct{}

// Checkers returns three entries: two violations (BasicLit key, BinaryExpr
// concat key) and one compliant string(<const>) key.
func (Res) Checkers() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"bare_literal_ready":    func(context.Context) error { return nil },
		"bad" + "_concat_ready": func(context.Context) error { return nil },
		string(ProbeReady):      func(context.Context) error { return nil },
	}
}
