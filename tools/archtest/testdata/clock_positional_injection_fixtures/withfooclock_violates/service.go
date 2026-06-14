// Package withfooclock_violates is a RED fixture for CLOCK-POSITIONAL-INJECTION-01.
// An exported function named WithFooClock (suffixed variant) that takes
// clock.Clock and returns an option type is present — sub-check B flags it.
// This proves the broadened predicate catches suffixed names beyond "WithClock".
package withfooclock_violates

import "github.com/ghbvf/gocell/framework/kernel/clock"

// Option configures Service.
type Option func(*Service)

// WithFooClock sets the clock on Service. This exported option-injector
// pattern with a suffixed name is banned by CLOCK-POSITIONAL-INJECTION-01
// sub-check B: migrate to a mandatory positional clock.Clock parameter instead.
func WithFooClock(clk clock.Clock) Option {
	return func(s *Service) { s.clk = clk }
}

// Service holds an injected clock.
type Service struct {
	clk clock.Clock
}
