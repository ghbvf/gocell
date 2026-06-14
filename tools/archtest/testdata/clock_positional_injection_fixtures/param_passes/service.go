// Package param_passes is the GREEN fixture for CLOCK-POSITIONAL-INJECTION-01.
// MustHaveClock is called with arg0 being a function parameter — compliant.
// No exported WithClock function is present — also compliant.
package param_passes

import "github.com/ghbvf/gocell/framework/kernel/clock"

// Service is a production-shaped struct that requires an injected Clock.
type Service struct {
	clk clock.Clock
}

// NewService is the compliant constructor: clk is a mandatory positional
// parameter and is passed directly to clock.MustHaveClock. arg0 is a param.
func NewService(clk clock.Clock) *Service {
	clock.MustHaveClock(clk, "param_passes.NewService") // compliant: clk is a param
	return &Service{clk: clk}
}

// withClockInternal is a package-private (unexported) function. It is NOT
// banned by sub-check B (which only bans exported WithClock functions).
func withClockInternal(_ clock.Clock) {}
