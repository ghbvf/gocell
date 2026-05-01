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

type typedNilReader struct{}

func (*typedNilReader) Read([]byte) (int, error) {
	return 0, errTypedNilReaderUsed
}

// TestMemStoreContract runs the full C1-C7 contract test suite (T1-T12) against
// the in-memory store. In the TDD red phase the memstore stubs return nil/nil,
// so the suite is expected to fail on most sub-tests.
func TestMemStoreContract(t *testing.T) {
	storetest.RunContractSuite(t, func(t *testing.T, policy refresh.Policy) (refresh.Store, *storetest.FakeClock) {
		clock := storetest.NewFakeClock(baseTime)
		// Pass nil for rand so memstore falls back to crypto/rand (fine for
		// the red phase; B3 may inject a deterministic reader for hermeticity).
		return memstore.MustNew(policy, clock, nil), clock
	})
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	clock := storetest.NewFakeClock(baseTime)

	tests := []struct {
		name   string
		policy refresh.Policy
		clock  refresh.Clock
	}{
		{
			name:   "nil clock",
			policy: refresh.Policy{ReuseInterval: time.Second, MaxAge: time.Hour},
			clock:  nil,
		},
		{
			name:   "non-positive max age",
			policy: refresh.Policy{ReuseInterval: time.Second},
			clock:  clock,
		},
		{
			name:   "negative reuse interval",
			policy: refresh.Policy{ReuseInterval: -time.Second, MaxAge: time.Hour},
			clock:  clock,
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
		refresh.Policy{ReuseInterval: time.Second, MaxAge: time.Hour},
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
		refresh.Policy{ReuseInterval: time.Second, MaxAge: time.Hour},
		clock,
		reader,
	)
	require.NoError(t, err)
	require.NotNil(t, store)

	_, _, err = store.Issue(context.Background(), "session-1", "subject-1")
	require.NoError(t, err)
}

func TestMustNewPanicsOnInvalidConfig(t *testing.T) {
	require.Panics(t, func() {
		memstore.MustNew(refresh.Policy{}, nil, nil)
	})
}
