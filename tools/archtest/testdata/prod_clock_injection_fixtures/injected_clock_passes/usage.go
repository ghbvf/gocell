// Package injected_clock_passes verifies that the canonical injected-Clock
// shape produces 0 violations. The local Clock interface is structurally
// satisfied by kernel/clock.Clock at the calling layer; we model that here
// without importing kernel/clock (the fixture module is standalone).
//
// Constants like time.Time, time.Duration, time.Second are types/consts and
// must NOT be flagged by the gate (filter is *types.Func only).
package injected_clock_passes

import "time"

// Clock mirrors the methods kernel/clock.Clock exposes; production callers
// would receive a kernel/clock.Clock value here. We never reference
// time.Now / time.Since / time.NewTimer / etc.
type Clock interface {
	Now() time.Time
	Since(t time.Time) time.Duration
	NewTimerAt(deadline time.Time) Timer
}

type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type Service struct {
	clk Clock
}

func New(clk Clock) *Service { return &Service{clk: clk} }

func (s *Service) Recent() time.Duration {
	return s.clk.Since(s.clk.Now().Add(-time.Second))
}

func (s *Service) Wait(deadline time.Time) {
	t := s.clk.NewTimerAt(deadline)
	defer t.Stop()
	<-t.C()
}
