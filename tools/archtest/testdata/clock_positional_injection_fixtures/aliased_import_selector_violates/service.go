// Package aliased_import_selector_violates is a RED fixture for
// CLOCK-POSITIONAL-INJECTION-01 sub-check A.
// The import alias `clk` is used for kernel/clock; MustHaveClock is called
// with cfg.Clock (a selector expression), not a direct parameter.
// This proves the type-resolved callee check handles import aliases correctly:
// `clk.MustHaveClock` resolves to the canonical kernel/clock.MustHaveClock via
// go/types regardless of the alias name.
package aliased_import_selector_violates

import clk "github.com/ghbvf/gocell/framework/kernel/clock"

// Config bundles dependencies in the old struct-injection pattern.
type Config struct {
	Clock clk.Clock
}

// Service requires a clock.
type Service struct {
	clock clk.Clock
}

// NewService uses aliased import and passes a selector (cfg.Clock) as arg0 to
// MustHaveClock. Sub-check A flags this: arg0 is not a direct parameter.
func NewService(cfg Config) *Service {
	clk.MustHaveClock(cfg.Clock, "aliased_import_selector_violates.NewService") // violation: cfg.Clock is a selector
	return &Service{clock: cfg.Clock}
}
