package postgres

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// typedNilSessionClock is used to pass a typed nil (non-nil interface, nil pointer)
// to NewPGSessionRepository to verify the nil-interface guard.
type typedNilSessionClock struct{}

func (*typedNilSessionClock) Now() time.Time                  { return time.Now() }
func (*typedNilSessionClock) Since(t time.Time) time.Duration { return time.Since(t) }
func (*typedNilSessionClock) Until(t time.Time) time.Duration { return time.Until(t) }
func (*typedNilSessionClock) NewTimerAt(t time.Time) clock.Timer {
	return clock.Real().NewTimerAt(t)
}

func (*typedNilSessionClock) NewTicker(d time.Duration) clock.Ticker {
	return clock.Real().NewTicker(d)
}

func (*typedNilSessionClock) AfterFunc(t time.Time, fn func()) clock.Timer {
	return clock.Real().AfterFunc(t, fn)
}

func (*typedNilSessionClock) Sleep(ctx context.Context, t time.Time) error {
	return clock.Real().Sleep(ctx, t)
}

// ---------------------------------------------------------------------------
// Constructor validation
// ---------------------------------------------------------------------------

func TestNewPGSessionRepository_RequiresPool(t *testing.T) {
	_, err := NewPGSessionRepository(nil, clock.Real())
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "pool must not be nil")
}

func TestNewPGSessionRepository_RequiresClock(t *testing.T) {
	_, err := NewPGSessionRepository(dummySessionPool(), nil)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "clock must not be nil")
}

func TestNewPGSessionRepository_TypedNilClock(t *testing.T) {
	// typed nil — interface is non-nil but the pointer inside is nil
	var clk *typedNilSessionClock
	_, err := NewPGSessionRepository(dummySessionPool(), clk)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "clock must not be nil")
}

func TestNewPGSessionRepository_HappyPath(t *testing.T) {
	repo, err := NewPGSessionRepository(dummySessionPool(), clock.Real())
	require.NoError(t, err)
	assert.NotNil(t, repo)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// dummySessionPool returns a non-nil *pgxpool.Pool sufficient to satisfy the
// nil-check in NewPGSessionRepository. The pool is never used for actual DB calls.
func dummySessionPool() *pgxpool.Pool {
	return new(pgxpool.Pool) //nolint:exhaustruct // dummy value for nil-check only
}
