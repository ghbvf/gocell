package memstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
	"github.com/ghbvf/gocell/runtime/auth/refresh/storetest"
)

// baseTime is the synthetic epoch for all FakeClocks in this test file.
var baseTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

var errTypedNilReaderUsed = errors.New("typed nil reader should have been defaulted")

type typedNilClock struct{}

func (*typedNilClock) Now() time.Time                  { return baseTime }
func (*typedNilClock) Since(t time.Time) time.Duration { return baseTime.Sub(t) }
func (*typedNilClock) Until(t time.Time) time.Duration { return t.Sub(baseTime) }
func (*typedNilClock) NewTimerAt(_ time.Time) clock.Timer {
	panic("typedNilClock.NewTimerAt not implemented")
}

func (*typedNilClock) NewTicker(_ time.Duration) clock.Ticker {
	panic("typedNilClock.NewTicker not implemented")
}

func (*typedNilClock) AfterFunc(_ time.Time, _ func()) clock.Timer {
	panic("typedNilClock.AfterFunc not implemented")
}

func (*typedNilClock) Sleep(_ context.Context, _ time.Time) error {
	panic("typedNilClock.Sleep not implemented")
}

type typedNilReader struct{}

func (*typedNilReader) Read([]byte) (int, error) {
	return 0, errTypedNilReaderUsed
}

// TestMemStoreContract runs the full C1-C7 contract test suite (T1-T12) against
// the in-memory store.
func TestMemStoreContract(t *testing.T) {
	storetest.RunContractSuite(t, func(t *testing.T, policy refresh.Policy) (refresh.Store, *storetest.FakeClock) {
		clk := storetest.NewFakeClock(baseTime)
		store, err := memstore.New(policy, clk, nil)
		require.NoError(t, err)
		return store, clk
	})
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	clk := storetest.NewFakeClock(baseTime)

	tests := []struct {
		name   string
		policy refresh.Policy
		clock  clock.Clock
	}{
		{
			name: "nil clock",
			policy: refresh.Policy{
				ReuseInterval:  time.Second,
				MaxAge:         time.Hour,
				MaxIdle:        refresh.DefaultMaxIdle,
				GraceMaxReuses: refresh.DefaultGraceMaxReuses,
			},
			clock: nil,
		},
		{
			name:   "non-positive max age",
			policy: refresh.Policy{ReuseInterval: time.Second},
			clock:  clk,
		},
		{
			name:   "negative reuse interval",
			policy: refresh.Policy{ReuseInterval: -time.Second, MaxAge: time.Hour},
			clock:  clk,
		},
		{
			name: "zero_MaxIdle",
			policy: refresh.Policy{
				ReuseInterval: time.Second,
				MaxAge:        time.Hour,
				// MaxIdle intentionally zero
				GraceMaxReuses: refresh.DefaultGraceMaxReuses,
			},
			clock: clk,
		},
		{
			name: "zero_GraceMaxReuses",
			policy: refresh.Policy{
				ReuseInterval: time.Second,
				MaxAge:        time.Hour,
				MaxIdle:       refresh.DefaultMaxIdle,
				// GraceMaxReuses intentionally zero
			},
			clock: clk,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			store, err := memstore.New(tc.policy, tc.clock, nil)
			require.Error(t, err)
			require.Nil(t, store)
		})
	}
}

func TestNewRejectsTypedNilClock(t *testing.T) {
	var clock *typedNilClock
	store, err := memstore.New(
		refresh.Policy{
			ReuseInterval:  time.Second,
			MaxAge:         time.Hour,
			MaxIdle:        refresh.DefaultMaxIdle,
			GraceMaxReuses: refresh.DefaultGraceMaxReuses,
		},
		clock,
		nil,
	)
	require.Error(t, err)
	require.Nil(t, store)
}

func TestNewDefaultsTypedNilRandReader(t *testing.T) {
	clock := storetest.NewFakeClock(baseTime)
	var reader *typedNilReader

	store, err := memstore.New(
		refresh.Policy{
			ReuseInterval:  time.Second,
			MaxAge:         time.Hour,
			MaxIdle:        refresh.DefaultMaxIdle,
			GraceMaxReuses: refresh.DefaultGraceMaxReuses,
		},
		clock,
		reader,
	)
	require.NoError(t, err)
	require.NotNil(t, store)

	_, _, err = store.Issue(context.Background(), "session-1", "subject-1")
	require.NoError(t, err)
}

func TestNewRejectsNilClock(t *testing.T) {
	t.Parallel()
	validPolicy := refresh.Policy{
		ReuseInterval:  time.Second,
		MaxAge:         time.Hour,
		MaxIdle:        refresh.DefaultMaxIdle,
		GraceMaxReuses: refresh.DefaultGraceMaxReuses,
	}
	_, err := memstore.New(validPolicy, nil, nil)
	require.Error(t, err, "New with nil clock must return error")
}

func TestNewRejectsEmptyPolicy(t *testing.T) {
	t.Parallel()
	fakeClock := storetest.NewFakeClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	_, err := memstore.New(refresh.Policy{}, fakeClock, nil)
	require.Error(t, err, "New with empty Policy must return error")
}
