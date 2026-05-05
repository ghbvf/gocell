package postgres

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
)

var errTypedNilRefreshReaderUsed = errors.New("typed nil refresh reader should have been defaulted")

// typedNilRefreshClock implements clock.Clock for typed-nil rejection tests.
// All methods are unreachable (the value is always typed-nil at the call
// site); they exist only to satisfy the clock.Clock interface.
type typedNilRefreshClock struct{}

func (*typedNilRefreshClock) Now() time.Time                  { return time.Now() }
func (*typedNilRefreshClock) Since(t time.Time) time.Duration { return time.Since(t) }
func (*typedNilRefreshClock) Until(t time.Time) time.Duration { return time.Until(t) }
func (*typedNilRefreshClock) NewTimerAt(t time.Time) clock.Timer {
	return clock.Real().NewTimerAt(t)
}

func (*typedNilRefreshClock) NewTicker(d time.Duration) clock.Ticker {
	return clock.Real().NewTicker(d)
}

func (*typedNilRefreshClock) AfterFunc(t time.Time, fn func()) clock.Timer {
	return clock.Real().AfterFunc(t, fn)
}

func (*typedNilRefreshClock) Sleep(ctx context.Context, t time.Time) error {
	return clock.Real().Sleep(ctx, t)
}

type typedNilRefreshReader struct{}

func (*typedNilRefreshReader) Read([]byte) (int, error) {
	return 0, errTypedNilRefreshReaderUsed
}

// dummyTxRunner is a minimal TxRunner for unit tests that do not hit the DB.
// It delegates fn directly with the provided context (no real transaction).
type dummyTxRunner struct{}

func (dummyTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// ---------------------------------------------------------------------------
// NewRefreshStore constructor validation
// ---------------------------------------------------------------------------

func TestNewRefreshStore_ReturnsErrorOnInvalidArgs(t *testing.T) {
	validClock := storetest.NewFakeClock(time.Now())
	validPolicy := refresh.Policy{
		ReuseInterval:  time.Second,
		MaxAge:         time.Hour,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}
	validTxRunner := dummyTxRunner{}

	// dummyPool is a non-nil *pgxpool.Pool used only to pass the nil check
	// in the constructor — never used for actual DB calls in these tests.
	dummyPool := new(pgxpool.Pool)

	t.Run("nil_pool", func(t *testing.T) {
		_, err := NewRefreshStore(nil, validTxRunner, validPolicy, validClock, nil)
		assert.Error(t, err)
	})

	t.Run("nil_txrunner", func(t *testing.T) {
		_, err := NewRefreshStore(dummyPool, nil, validPolicy, validClock, nil)
		assert.Error(t, err)
	})

	t.Run("nil_clock", func(t *testing.T) {
		_, err := NewRefreshStore(dummyPool, validTxRunner, validPolicy, nil, nil)
		assert.Error(t, err)
	})

	t.Run("typed_nil_clock", func(t *testing.T) {
		var clk *typedNilRefreshClock
		_, err := NewRefreshStore(dummyPool, validTxRunner, validPolicy, clk, nil)
		assert.Error(t, err)
	})

	t.Run("zero_MaxAge", func(t *testing.T) {
		p := refresh.Policy{ReuseInterval: time.Second, MaxAge: 0}
		_, err := NewRefreshStore(dummyPool, validTxRunner, p, validClock, nil)
		assert.Error(t, err)
	})

	t.Run("negative_MaxAge", func(t *testing.T) {
		p := refresh.Policy{ReuseInterval: time.Second, MaxAge: -time.Hour}
		_, err := NewRefreshStore(dummyPool, validTxRunner, p, validClock, nil)
		assert.Error(t, err)
	})

	t.Run("negative_ReuseInterval", func(t *testing.T) {
		p := refresh.Policy{ReuseInterval: -time.Second, MaxAge: time.Hour}
		_, err := NewRefreshStore(dummyPool, validTxRunner, p, validClock, nil)
		assert.Error(t, err)
	})
}

func TestNewRefreshStore_NilRandReader_UsesDefault(t *testing.T) {
	dummyPool := new(pgxpool.Pool)
	validClock := storetest.NewFakeClock(time.Now())
	validPolicy := refresh.Policy{
		ReuseInterval:  time.Second,
		MaxAge:         time.Hour,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}
	validTxRunner := dummyTxRunner{}

	// nil randReader must not error — constructor falls back to crypto/rand.Reader.
	s, err := NewRefreshStore(dummyPool, validTxRunner, validPolicy, validClock, nil)
	require.NoError(t, err)
	assert.NotNil(t, s.rand, "rand field must be non-nil after constructor")
}

func TestNewRefreshStore_TypedNilRandReader_UsesDefault(t *testing.T) {
	dummyPool := new(pgxpool.Pool)
	validClock := storetest.NewFakeClock(time.Now())
	validPolicy := refresh.Policy{
		ReuseInterval:  time.Second,
		MaxAge:         time.Hour,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}
	validTxRunner := dummyTxRunner{}
	var reader *typedNilRefreshReader

	s, err := NewRefreshStore(dummyPool, validTxRunner, validPolicy, validClock, reader)
	require.NoError(t, err)
	_, _, err = s.generatePair()
	require.NoError(t, err)
}

// ---------------------------------------------------------------------------
// generatePair — pure function, controllable via io.Reader
// ---------------------------------------------------------------------------

func TestGeneratePair_Lengths(t *testing.T) {
	// 16 + 32 zero bytes → selector 16 bytes, verifier 32 bytes.
	src := bytes.NewReader(make([]byte, refresh.SelectorLen+refresh.VerifierLen))
	s := &PGRefreshStore{rand: src}

	sel, ver, err := s.generatePair()
	require.NoError(t, err)
	assert.Len(t, sel, refresh.SelectorLen, "selector must be %d bytes", refresh.SelectorLen)
	assert.Len(t, ver, refresh.VerifierLen, "verifier must be %d bytes", refresh.VerifierLen)
}

func TestGeneratePair_ReaderError(t *testing.T) {
	sentinel := errors.New("entropy source exhausted")
	s := &PGRefreshStore{rand: &errorReader{err: sentinel}}

	_, _, err := s.generatePair()
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel, "reader error must be wrapped and surfaced")
}

// ---------------------------------------------------------------------------
// Test helpers
// ---------------------------------------------------------------------------

// errorReader always returns the configured error from Read.
type errorReader struct{ err error }

func (r *errorReader) Read(_ []byte) (int, error) { return 0, r.err }
