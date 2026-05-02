package testutil

import (
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
)

// defaultTestEpoch is the fixed start time used when no clock is provided.
// Using a fixed epoch keeps test fixtures deterministic across runs.
var defaultTestEpoch = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// sessionRepoOptions holds configuration for NewSessionRepoForTest.
type sessionRepoOptions struct {
	clk clock.Clock
}

// Option configures NewSessionRepoForTest.
type Option func(*sessionRepoOptions)

// WithClock injects a custom clock into the session repository.
// Use this when the test needs to drive time-dependent logic via FakeClock.Advance.
func WithClock(clk clock.Clock) Option {
	return func(o *sessionRepoOptions) {
		o.clk = clk
	}
}

// RealSessionRepo is a convenience wrapper around NewSessionRepoForTest that
// injects clock.Real(). Use this when the test does not need to drive time
// via FakeClock.Advance — keeps callsites concise (lll-friendly).
func RealSessionRepo(t testing.TB) ports.SessionRepository {
	t.Helper()
	return NewSessionRepoForTest(t, WithClock(clock.Real()))
}

// NewSessionRepoForTest creates an in-memory SessionRepository for use in tests.
// By default it uses a FakeClock starting at 2026-01-01 UTC so session expiry
// paths can be driven deterministically via FakeClock.Advance.
//
// To obtain the underlying FakeClock, create it externally and inject via WithClock:
//
//	clk := clockmock.New(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
//	repo := testutil.NewSessionRepoForTest(t, testutil.WithClock(clk))
//	clk.Advance(2 * time.Hour) // drive expiry
//
// Accepts testing.TB so it works in both *testing.T and *testing.B contexts.
func NewSessionRepoForTest(t testing.TB, opts ...Option) ports.SessionRepository {
	t.Helper()
	o := &sessionRepoOptions{}
	for _, opt := range opts {
		opt(o)
	}
	if o.clk == nil {
		o.clk = clockmock.New(defaultTestEpoch)
	}
	return mem.NewSessionRepository(o.clk)
}
