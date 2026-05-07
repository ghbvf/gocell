//go:build integration

package redis

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
)

// race-stress test goroutine count. Stays well within go-redis's default
// PoolSize of 10×GOMAXPROCS for standalone clients while still amplifying
// timing windows under -race + -count=N. Used by every test in this file
// — a single file-level constant keeps contention level uniform across
// the four primitives.
const raceStressGoroutines = 50

// TestRaceStress_DistLock_SetNX_ExactlyOneWinner asserts that 50 concurrent
// SetNX calls on the same key (under -race) produce exactly one winner.
// Complements the existing locktest conformC4 case by adding the race
// detector + the namespace prefix derivation under contention.
func TestRaceStress_DistLock_SetNX_ExactlyOneWinner(t *testing.T) {
	client, cleanup := startRedis(t)
	defer cleanup()

	drv, err := NewRedisDriver(client.cmdable(), testNamespace)
	require.NoError(t, err)
	ctx := context.Background()
	key := fmt.Sprintf("race:distlock:setnx:%s", t.Name())

	var (
		wg      sync.WaitGroup
		winners atomic.Int64
	)
	wg.Add(raceStressGoroutines)
	for i := range raceStressGoroutines {
		go func(i int) {
			defer wg.Done()
			ok, err := drv.SetNX(ctx, key, fmt.Sprintf("token-%d", i), testtime.EventuallyLong)
			if err != nil {
				t.Errorf("goroutine %d: SetNX returned err: %v", i, err)
				return
			}
			if ok {
				winners.Add(1)
			}
		}(i)
	}
	wg.Wait()

	assert.Equal(t, int64(1), winners.Load(),
		"exactly one of %d concurrent SetNX must win the lock", raceStressGoroutines)

	// Pin the wire-level key shape: namespace prefix MUST be operative under
	// concurrency. Without this, an empty / mis-derived namespace would still
	// pass the exactly-one-winner assertion above. Get returns the holder
	// token (any non-empty string) when the prefixed key exists.
	prefixed := string(testNamespace) + ":" + key
	got, err := client.cmdable().Get(ctx, prefixed).Result()
	require.NoError(t, err, "lock key must exist under namespace prefix %q", prefixed)
	assert.NotEmpty(t, got, "lock key value (token) under prefix %q must be non-empty", prefixed)
}

// TestRaceStress_IdempotencyClaimer_ExactlyOneAcquired asserts that 50
// concurrent Claim calls on the same business key acquire exactly one
// lease, and a subsequent wave (after Commit) sees ClaimDone for all.
func TestRaceStress_IdempotencyClaimer_ExactlyOneAcquired(t *testing.T) {
	client, cleanup := startRedis(t)
	defer cleanup()

	claimer, err := NewIdempotencyClaimer(client, testNamespace)
	require.NoError(t, err)
	ctx := context.Background()
	bizKey := fmt.Sprintf("race:idempotency:%s", t.Name())

	type result struct {
		state   idempotency.ClaimState
		receipt idempotency.Receipt
	}
	results := make(chan result, raceStressGoroutines)
	var wg sync.WaitGroup
	wg.Add(raceStressGoroutines)
	for range raceStressGoroutines {
		go func() {
			defer wg.Done()
			state, receipt, err := claimer.Claim(ctx, bizKey, testtime.D5min, testtime.D24h)
			if err != nil {
				t.Errorf("Claim returned err: %v", err)
				return
			}
			results <- result{state: state, receipt: receipt}
		}()
	}
	wg.Wait()
	close(results)

	var (
		acquired int
		busy     int
		done     int
		winner   idempotency.Receipt
	)
	for r := range results {
		switch r.state {
		case idempotency.ClaimAcquired:
			acquired++
			winner = r.receipt
		case idempotency.ClaimBusy:
			busy++
		case idempotency.ClaimDone:
			done++
		}
	}
	assert.Equalf(t, 1, acquired,
		"exactly one of %d concurrent Claim calls must return ClaimAcquired (got acquired=%d busy=%d done=%d)",
		raceStressGoroutines, acquired, busy, done)
	require.NotNil(t, winner, "winner receipt must be non-nil")
	require.NoError(t, winner.Commit(ctx), "Commit on winning receipt must succeed")

	// Pin the wire-level done-key shape post-Commit: prefix outside the
	// hashtag, role suffix `:done`. An empty namespace or a hashtag
	// regression would surface as a Get miss here.
	doneKey := string(testNamespace) + ":{" + bizKey + "}:done"
	got, err := client.cmdable().Get(ctx, doneKey).Result()
	require.NoError(t, err, "done key must exist post-Commit at %q", doneKey)
	assert.Equal(t, "1", got, "done sentinel value must be \"1\"")

	// Second wave: every Claim should now see ClaimDone.
	results2 := make(chan idempotency.ClaimState, raceStressGoroutines)
	var wg2 sync.WaitGroup
	wg2.Add(raceStressGoroutines)
	for range raceStressGoroutines {
		go func() {
			defer wg2.Done()
			state, _, err := claimer.Claim(ctx, bizKey, testtime.D5min, testtime.D24h)
			if err != nil {
				t.Errorf("Claim returned err: %v", err)
				return
			}
			results2 <- state
		}()
	}
	wg2.Wait()
	close(results2)
	doneAfterCommit := 0
	for state := range results2 {
		if state == idempotency.ClaimDone {
			doneAfterCommit++
		}
	}
	assert.Equal(t, raceStressGoroutines, doneAfterCommit,
		"after Commit all subsequent Claim calls must return ClaimDone")
}

// TestRaceStress_NonceStore_ExactlyOneSuccess asserts that 50 concurrent
// CheckAndMark calls for the same nonce return nil for exactly one caller
// and ErrNonceReused for the rest. nonceTestNamespace is defined in
// nonce_test.go (same package) — distinct from testNamespace so cross-prefix
// regressions surface as a missed lookup.
func TestRaceStress_NonceStore_ExactlyOneSuccess(t *testing.T) {
	client, cleanup := startRedis(t)
	defer cleanup()

	store, err := NewNonceStore(client, nonceTestNamespace, auth.ServiceTokenNonceTTL)
	require.NoError(t, err)
	ctx := context.Background()
	nonce := fmt.Sprintf("race-nonce-%d", time.Now().UnixNano())

	var (
		wg       sync.WaitGroup
		first    atomic.Int64
		replays  atomic.Int64
		fatalErr atomic.Pointer[error]
	)
	wg.Add(raceStressGoroutines)
	for range raceStressGoroutines {
		go func() {
			defer wg.Done()
			err := store.CheckAndMark(ctx, nonce)
			switch {
			case err == nil:
				first.Add(1)
			case errors.Is(err, auth.ErrNonceReused):
				replays.Add(1)
			default:
				fatalErr.Store(&err)
			}
		}()
	}
	wg.Wait()

	if p := fatalErr.Load(); p != nil {
		t.Fatalf("unexpected non-replay error: %v", *p)
	}
	assert.Equal(t, int64(1), first.Load(),
		"exactly one of %d concurrent CheckAndMark must succeed", raceStressGoroutines)
	assert.Equal(t, int64(raceStressGoroutines-1), replays.Load(),
		"all remaining CheckAndMark must return ErrNonceReused")
}

// TestRaceStress_Cache_ConcurrentSetGet_NoDataRace exercises Cache under
// concurrent reads and writes to surface adapter-side data races (the
// race detector is the primary assertion). Functional invariants: no
// goroutine returns an error, and every Get on a freshly-Set key returns
// either the value just written or the empty string (never garbage).
func TestRaceStress_Cache_ConcurrentSetGet_NoDataRace(t *testing.T) {
	client, cleanup := startRedis(t)
	defer cleanup()

	cache, err := NewCache(client, testNamespace)
	require.NoError(t, err)
	ctx := context.Background()

	var wg sync.WaitGroup
	wg.Add(raceStressGoroutines)
	for i := range raceStressGoroutines {
		go func(i int) {
			defer wg.Done()
			key := fmt.Sprintf("race:cache:%s:%d", t.Name(), i)
			value := fmt.Sprintf("value-%d", i)
			if err := cache.Set(ctx, key, value, testtime.EventuallyLong); err != nil {
				t.Errorf("goroutine %d: Set: %v", i, err)
				return
			}
			got, err := cache.Get(ctx, key)
			if err != nil {
				t.Errorf("goroutine %d: Get: %v", i, err)
				return
			}
			if got != value && got != "" {
				t.Errorf("goroutine %d: Get returned unexpected value %q", i, got)
			}
		}(i)
	}
	wg.Wait()
}
