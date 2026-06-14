// Package ctx_param_passes is a GREEN fixture for CLOCK-POSITIONAL-INJECTION-01.
// MustHaveClock is called with arg0 as clk, which is a function parameter at
// position 1 (after ctx context.Context). This confirms the sub-check A param
// lookup covers non-first positional parameters correctly.
// No exported With*Clock option-injector is present — sub-check B also passes.
package ctx_param_passes

import (
	"context"

	"github.com/ghbvf/gocell/framework/kernel/clock"
)

// Service holds an injected clock.
type Service struct {
	clk clock.Clock
}

// NewService is compliant: clk is a mandatory positional parameter at position 1
// (after ctx). arg0 to MustHaveClock is a direct parameter reference — compliant.
func NewService(ctx context.Context, clk clock.Clock) *Service {
	clock.MustHaveClock(clk, "ctx_param_passes.NewService") // compliant: clk is a param
	_ = ctx
	return &Service{clk: clk}
}
