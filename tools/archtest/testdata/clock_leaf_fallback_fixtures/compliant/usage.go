// Package compliant exercises the KERNEL-CLOCK-LEAF-FALLBACK-01 positive path:
// production-shaped code that receives a clock.Clock through its constructor
// and uses it without ever calling clock.Real(). The gate must report 0
// violations.
//
// The fixture also references unrelated identifiers named "Real" (a struct,
// a method on an unrelated type, a variable) to verify the gate's
// type-driven filter only matches the package-level kernel/clock.Real
// function.
package compliant

import (
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
)

// Service holds an injected clock and exposes derived time queries.
type Service struct {
	c clock.Clock
}

// NewService is the canonical compliant shape: clock is required at
// construction time. clock.MustHaveClock panics on nil/typed-nil, which is
// the project convention for Must* fail-fast (see PANIC-REGISTERED-01).
// Production code that needs error semantics performs a nil check itself
// before calling MustHaveClock — never falls back to clock.Real().
func NewService(c clock.Clock) *Service {
	clock.MustHaveClock(c, "compliant.NewService")
	return &Service{c: c}
}

// Now reads the injected clock — never reaches kernel/clock.Real().
func (s *Service) Now() time.Time { return s.c.Now() }

// Real is an unrelated identifier intentionally named "Real" to verify the
// gate filters by package-level *types.Func from kernel/clock — methods on
// an unrelated type must not be flagged.
type Real struct{}

func (Real) Get() string { return "not the kernel/clock.Real factory" }
