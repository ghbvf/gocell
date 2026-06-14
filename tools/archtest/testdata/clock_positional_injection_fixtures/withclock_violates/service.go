// Package withclock_violates is a RED fixture for CLOCK-POSITIONAL-INJECTION-01.
// An exported function named WithClock that takes clock.Clock and returns an
// option type is present — sub-check B flags it.
package withclock_violates

import "github.com/ghbvf/gocell/framework/kernel/clock"

// Option configures Service.
type Option func(*Service)

// WithClock sets the clock on Service. This exported option-injector pattern
// is banned by CLOCK-POSITIONAL-INJECTION-01 sub-check B: migrate to a
// mandatory positional clock.Clock parameter instead.
func WithClock(clk clock.Clock) Option {
	return func(s *Service) { s.clk = clk }
}

// Service holds an injected clock.
type Service struct {
	clk clock.Clock
}
