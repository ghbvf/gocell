// Package struct_field_violates is a RED fixture for CLOCK-POSITIONAL-INJECTION-01
// sub-check C (#1136 review F1).
//
// An exported Config-suffixed struct declares an exported Clock field of type
// kernel/clock.Clock. External callers can construct `BarConfig{Clock: nil}`
// and bypass the positional injection promise — sub-check C flags this.
package struct_field_violates

import "github.com/ghbvf/gocell/kernel/clock"

// BarConfig is an input Config struct. Its exported Clock field violates
// sub-check C: clock must enter via positional parameter, not struct literal.
type BarConfig struct {
	Clock clock.Clock
	Name  string
}

// BarOptions is a second input Options struct — same violation shape.
type BarOptions struct {
	Clock clock.Clock
}

// BarOpts uses the third sanctioned suffix.
type BarOpts struct {
	Clock clock.Clock
}
