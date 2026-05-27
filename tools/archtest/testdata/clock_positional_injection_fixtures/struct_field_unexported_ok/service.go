// Package struct_field_unexported_ok is the GREEN reverse self-check for
// CLOCK-POSITIONAL-INJECTION-01 sub-check C (#1136 review F1 blind-spot
// inventory).
//
// This fixture documents the two carve-outs by construction:
//
//	(a) Unexported struct + unexported clock field — the framework's
//	    option-pattern accumulator shape used by runtime/auth.authConfig,
//	    runtime/auth.serviceTokenConfig, and runtime/config.watcherConfig.
//	    The struct cannot be constructed by external callers, and the clock
//	    field is populated from a positional parameter inside the package.
//
//	(b) Non-Config/Options/Opts suffix — exported holder structs such as
//	    cmd/corebundle.SharedDeps. The threading source is allowed to hold
//	    the canonical Clock instance per ADR 202605270000 §Decision #4.
//
//	(c) Internal state struct with positional constructor — runtime types
//	    such as runtime/command.SweeperLifecycle.BusinessClock. The field is
//	    not part of an input struct name; the constructor takes positional
//	    clock and assigns it into the state.
//
// All three shapes are GREEN (empty diag.golden).
package struct_field_unexported_ok

import "github.com/ghbvf/gocell/kernel/clock"

// (a) unexported option-pattern accumulator: BOTH struct AND field unexported.
type fooConfig struct {
	clock clock.Clock
	name  string
}

// (b) Exported composition-root holder. Non-Config/Options/Opts suffix —
// excluded by naming convention alone (no allowlist needed).
type SharedDeps struct {
	Clock clock.Clock
}

// (c) Non-Config suffix runtime state: positional constructor.
type Service struct {
	clk clock.Clock
}

// NewService is the positional constructor; clk is the framework's required
// positional parameter, not an input struct field.
func NewService(clk clock.Clock) *Service {
	return &Service{clk: clk}
}
