// Package violates exercises the KERNEL-CLOCK-LEAF-FALLBACK-01 negative path:
// production-shaped code that calls kernel/clock.Real() outside the composition
// root. The gate must report exactly 3 violations: the standard form, an
// import-alias form, and a fallback-default form inside a constructor body.
package violates

import (
	"github.com/ghbvf/gocell/kernel/clock"
	clk "github.com/ghbvf/gocell/kernel/clock"
)

// directCall — standard form: pkg.Real() with the canonical import name.
func directCall() clock.Clock {
	return clock.Real()
}

// aliasCall — import-alias form: same function reached via a renamed import.
// Resolution is type-driven, so the alias must still be flagged.
func aliasCall() clk.Clock {
	return clk.Real()
}

// Service is a production-shaped struct with a "if nil { fallback }" pattern.
type Service struct {
	c clock.Clock
}

// NewService is the canonical leaf-fallback violation: silently substituting
// clock.Real() when the caller forgets to inject. The whole point of the gate
// is to forbid this exact shape.
func NewService(c clock.Clock) *Service {
	if c == nil {
		c = clock.Real()
	}
	return &Service{c: c}
}
