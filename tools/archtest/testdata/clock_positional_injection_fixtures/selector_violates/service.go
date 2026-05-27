// Package selector_violates is a RED fixture for CLOCK-POSITIONAL-INJECTION-01.
// MustHaveClock is called with arg0 as a selector expression (cfg.Clock),
// not a direct parameter — violation.
package selector_violates

import "github.com/ghbvf/gocell/kernel/clock"

// Config bundles dependencies in the old struct-injection pattern.
type Config struct {
	Clock clock.Clock
}

// Service requires a clock.
type Service struct {
	clk clock.Clock
}

// NewService uses the old config/struct-injection pattern: arg0 is cfg.Clock
// (a selector expression, not a parameter). Sub-check A flags this.
func NewService(cfg Config) *Service {
	clock.MustHaveClock(cfg.Clock, "selector_violates.NewService") // violation: cfg.Clock is a selector
	return &Service{clk: cfg.Clock}
}
