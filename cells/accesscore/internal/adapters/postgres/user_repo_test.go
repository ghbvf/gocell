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

// typedNilUserClock is used to pass a typed nil (non-nil interface, nil pointer)
// to NewPGUserRepository to verify the nil-interface guard.
type typedNilUserClock struct{}

func (*typedNilUserClock) Now() time.Time                  { return time.Now() }
func (*typedNilUserClock) Since(t time.Time) time.Duration { return time.Since(t) }
func (*typedNilUserClock) Until(t time.Time) time.Duration { return time.Until(t) }
func (*typedNilUserClock) NewTimerAt(t time.Time) clock.Timer {
	return clock.Real().NewTimerAt(t)
}
func (*typedNilUserClock) NewTicker(d time.Duration) clock.Ticker {
	return clock.Real().NewTicker(d)
}
func (*typedNilUserClock) AfterFunc(t time.Time, fn func()) clock.Timer {
	return clock.Real().AfterFunc(t, fn)
}
func (*typedNilUserClock) Sleep(ctx context.Context, t time.Time) error {
	return clock.Real().Sleep(ctx, t)
}

// ---------------------------------------------------------------------------
// Constructor validation
// ---------------------------------------------------------------------------

func TestNewPGUserRepository_RequiresPool(t *testing.T) {
	_, err := NewPGUserRepository(nil, clock.Real())
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "pool must not be nil")
}

func TestNewPGUserRepository_RequiresClock(t *testing.T) {
	// nil untyped interface
	_, err := NewPGUserRepository(dummyUserPool(), nil)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "clock must not be nil")
}

func TestNewPGUserRepository_TypedNilClock(t *testing.T) {
	// typed nil — interface is non-nil but the pointer inside is nil
	var clk *typedNilUserClock
	_, err := NewPGUserRepository(dummyUserPool(), clk)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, ec.Message, "clock must not be nil")
}

func TestNewPGUserRepository_HappyPath(t *testing.T) {
	repo, err := NewPGUserRepository(dummyUserPool(), clock.Real())
	require.NoError(t, err)
	assert.NotNil(t, repo)
}

// ---------------------------------------------------------------------------
// helpers
// ---------------------------------------------------------------------------

// dummyUserPool returns a non-nil *pgxpool.Pool sufficient to satisfy the
// nil-check in NewPGUserRepository. The pool is never used for actual DB calls.
func dummyUserPool() *pgxpool.Pool {
	return new(pgxpool.Pool) //nolint:exhaustruct // dummy value for nil-check only
}
