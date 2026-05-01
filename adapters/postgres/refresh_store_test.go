package postgres

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
)

var errTypedNilRefreshReaderUsed = errors.New("typed nil refresh reader should have been defaulted")

type typedNilRefreshClock struct{}

func (*typedNilRefreshClock) Now() time.Time {
	return time.Now()
}

type typedNilRefreshReader struct{}

func (*typedNilRefreshReader) Read([]byte) (int, error) {
	return 0, errTypedNilRefreshReaderUsed
}

// ---------------------------------------------------------------------------
// NewRefreshStore constructor validation
// ---------------------------------------------------------------------------

func TestNewRefreshStore_ReturnsErrorOnInvalidArgs(t *testing.T) {
	validClock := storetest.NewFakeClock(time.Now())
	validPolicy := refresh.Policy{ReuseInterval: time.Second, MaxAge: time.Hour}

	// dummyPool is a non-nil *pgxpool.Pool used only to pass the nil check
	// in the constructor — never used for actual DB calls in these tests.
	dummyPool := new(pgxpool.Pool)

	t.Run("nil_pool", func(t *testing.T) {
		_, err := NewRefreshStore(nil, validPolicy, validClock, nil)
		assert.Error(t, err)
	})

	t.Run("nil_clock", func(t *testing.T) {
		_, err := NewRefreshStore(dummyPool, validPolicy, nil, nil)
		assert.Error(t, err)
	})

	t.Run("typed_nil_clock", func(t *testing.T) {
		var clock *typedNilRefreshClock
		_, err := NewRefreshStore(dummyPool, validPolicy, clock, nil)
		assert.Error(t, err)
	})

	t.Run("zero_MaxAge", func(t *testing.T) {
		p := refresh.Policy{ReuseInterval: time.Second, MaxAge: 0}
		_, err := NewRefreshStore(dummyPool, p, validClock, nil)
		assert.Error(t, err)
	})

	t.Run("negative_MaxAge", func(t *testing.T) {
		p := refresh.Policy{ReuseInterval: time.Second, MaxAge: -time.Hour}
		_, err := NewRefreshStore(dummyPool, p, validClock, nil)
		assert.Error(t, err)
	})

	t.Run("negative_ReuseInterval", func(t *testing.T) {
		p := refresh.Policy{ReuseInterval: -time.Second, MaxAge: time.Hour}
		_, err := NewRefreshStore(dummyPool, p, validClock, nil)
		assert.Error(t, err)
	})
}

func TestMustNewRefreshStore_PanicsOnNilPool(t *testing.T) {
	validClock := storetest.NewFakeClock(time.Now())
	validPolicy := refresh.Policy{ReuseInterval: time.Second, MaxAge: time.Hour}
	assert.Panics(t, func() {
		MustNewRefreshStore(nil, validPolicy, validClock, nil)
	})
}

func TestNewRefreshStore_NilRandReader_UsesDefault(t *testing.T) {
	dummyPool := new(pgxpool.Pool)
	validClock := storetest.NewFakeClock(time.Now())
	validPolicy := refresh.Policy{ReuseInterval: time.Second, MaxAge: time.Hour}

	// nil randReader must not error — constructor falls back to crypto/rand.Reader.
	s, err := NewRefreshStore(dummyPool, validPolicy, validClock, nil)
	require.NoError(t, err)
	assert.NotNil(t, s.rand, "rand field must be non-nil after constructor")
}

func TestNewRefreshStore_TypedNilRandReader_UsesDefault(t *testing.T) {
	dummyPool := new(pgxpool.Pool)
	validClock := storetest.NewFakeClock(time.Now())
	validPolicy := refresh.Policy{ReuseInterval: time.Second, MaxAge: time.Hour}
	var reader *typedNilRefreshReader

	s, err := NewRefreshStore(dummyPool, validPolicy, validClock, reader)
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
