package postgres

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ghbvf/gocell/kernel/clock"
)

// typedNilClock implements clock.Clock but holds a nil pointer.
// Used to verify that repository constructors that accept clock.Clock reject
// typed-nil interface values (validation.IsNilInterface guard).
type typedNilClock struct{}

func (*typedNilClock) Now() time.Time                  { return time.Now() }
func (*typedNilClock) Since(t time.Time) time.Duration { return time.Since(t) }
func (*typedNilClock) Until(t time.Time) time.Duration { return time.Until(t) }
func (*typedNilClock) NewTimerAt(t time.Time) clock.Timer {
	return clock.Real().NewTimerAt(t)
}

func (*typedNilClock) NewTicker(d time.Duration) clock.Ticker {
	return clock.Real().NewTicker(d)
}

func (*typedNilClock) AfterFunc(t time.Time, fn func()) clock.Timer {
	return clock.Real().AfterFunc(t, fn)
}

func (*typedNilClock) Sleep(ctx context.Context, t time.Time) error {
	return clock.Real().Sleep(ctx, t)
}

// dummyPool returns a non-nil *pgxpool.Pool sufficient to satisfy the nil-check
// in repository constructors. The pool is never used for actual DB calls in unit tests.
func dummyPool() *pgxpool.Pool {
	return new(pgxpool.Pool) //nolint:exhaustruct // dummy value for nil-check only
}
