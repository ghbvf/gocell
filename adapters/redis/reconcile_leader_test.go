package redis

import (
	"context"
	"strconv"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/reconcile"
	"github.com/ghbvf/gocell/kernel/reconcile/reconciletest"
)

// Compile-time assertion: *RedisReconcileElector satisfies reconcile.LeaderElector.
var _ reconcile.LeaderElector = (*RedisReconcileElector)(nil)

// reconcileMockCmdable simulates the reconcile elector's Lua scripts in-memory so
// the conformance suite runs deterministically without a live Redis. It reuses
// the base mockCmdable store + ownership-guarded simulateScript (renew/release)
// and adds the 2-key acquire script (SET-NX + epoch INCR). Faithful Lua atomicity
// is validated separately against real Redis in reconcile_leader_integration_test.go.
type reconcileMockCmdable struct {
	mockCmdable
}

func newReconcileMock() *reconcileMockCmdable {
	return &reconcileMockCmdable{mockCmdable: mockCmdable{store: make(map[string]mockEntry)}}
}

func (m *reconcileMockCmdable) Eval(_ context.Context, script string, keys []string, args ...any) *goredis.Cmd {
	cmd := goredis.NewCmd(context.Background())
	if m.evalErr != nil {
		cmd.SetErr(m.evalErr)
		return cmd
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case script == reconcileAcquireScript: // 2-key {acquired, epoch}
		m.evalReconcileAcquire(cmd, keys, args)
	case script == reconcileRenewScript: // 2-key ownership-guarded TTL refresh → 1/0
		m.evalReconcileRenew(cmd, keys, args)
	case len(keys) == 1: // release (releaseLockScript): reuse base ownership-guarded sim
		cmd.SetVal(m.simulateScript(script, keys, args))
	default:
		cmd.SetVal(int64(0))
	}
	return cmd
}

// evalReconcileRenew mirrors reconcileRenewScript (ownership-guarded refresh of
// both holder + epoch key TTL). Caller holds m.mu.
func (m *reconcileMockCmdable) evalReconcileRenew(cmd *goredis.Cmd, keys []string, args []any) {
	holderKey := keys[0]
	holderID := toString(args[0])
	ttl := time.Duration(toInt64(args[1])) * time.Millisecond
	if cur, live := m.liveValue(holderKey); live && cur == holderID {
		m.store[holderKey] = mockEntry{value: holderID, expiry: time.Now().Add(ttl)}
		cmd.SetVal(int64(1))
		return
	}
	cmd.SetVal(int64(0))
}

// evalReconcileAcquire mirrors reconcileAcquireScript. Caller holds m.mu.
func (m *reconcileMockCmdable) evalReconcileAcquire(cmd *goredis.Cmd, keys []string, args []any) {
	holderKey, epochKey := keys[0], keys[1]
	holderID := toString(args[0])
	ttl := time.Duration(toInt64(args[1])) * time.Millisecond

	cur, live := m.liveValue(holderKey)
	switch {
	case !live: // free / expired → take it and bump epoch
		m.store[holderKey] = mockEntry{value: holderID, expiry: time.Now().Add(ttl)}
		cmd.SetVal([]any{int64(1), m.incrEpoch(epochKey)})
	case cur == holderID: // idempotent same-holder re-acquire → keep epoch, refresh
		m.store[holderKey] = mockEntry{value: holderID, expiry: time.Now().Add(ttl)}
		cmd.SetVal([]any{int64(1), m.readEpoch(epochKey)})
	default: // held by another holder
		cmd.SetVal([]any{int64(0), int64(0)})
	}
}

// liveValue returns (value, true) when key exists and is not expired, treating an
// expired key as absent (matching Redis GET-after-PX semantics).
func (m *reconcileMockCmdable) liveValue(key string) (string, bool) {
	e, ok := m.store[key]
	if !ok {
		return "", false
	}
	if !e.expiry.IsZero() && time.Now().After(e.expiry) {
		delete(m.store, key)
		return "", false
	}
	return e.value, true
}

func (m *reconcileMockCmdable) incrEpoch(epochKey string) int64 {
	e := m.readEpoch(epochKey) + 1
	m.store[epochKey] = mockEntry{value: strconv.FormatInt(e, 10)} // no expiry — epoch persists
	return e
}

func (m *reconcileMockCmdable) readEpoch(epochKey string) int64 {
	if e, ok := m.store[epochKey]; ok {
		n, _ := strconv.ParseInt(e.value, 10, 64)
		return n
	}
	return 0
}

// mustElector builds a mock-backed elector. holderID is minted internally (each
// call → a distinct UUID), so two electors over the same mock contend as distinct
// holders without a caller-supplied label.
func mustElector(t *testing.T, rdb cmdable) *RedisReconcileElector {
	t.Helper()
	e, err := newReconcileElectorFromCmdable(rdb, "reconcile", 30*time.Second, clock.Real())
	require.NoError(t, err)
	return e
}

// TestReconcileElector_Conformance runs the cross-implementation suites against a
// mock-backed redis elector (deterministic, PR-time CI). Real-Redis Lua atomicity
// is covered by reconcile_leader_integration_test.go.
func TestReconcileElector_Conformance(t *testing.T) {
	mock := newReconcileMock()
	factory := func(string) reconcile.LeaderElector { return mustElector(t, mock) }
	t.Run("Leader", func(t *testing.T) { reconciletest.RunLeaderConformance(t, factory) })

	mock2 := newReconcileMock()
	factory2 := func(string) reconcile.LeaderElector { return mustElector(t, mock2) }
	t.Run("Fencing", func(t *testing.T) { reconciletest.RunFencingConformance(t, factory2) })
}

// TestReconcileElector_ContentionReportsLeaseHeld verifies a denied acquire maps
// to reconcile.ErrLeaseHeld (logged at Debug by the Loop).
func TestReconcileElector_ContentionReportsLeaseHeld(t *testing.T) {
	mock := newReconcileMock()
	a, b := mustElector(t, mock), mustElector(t, mock)
	ctx := context.Background()
	_, err := a.AcquireLease(ctx, "rid")
	require.NoError(t, err)
	_, err = b.AcquireLease(ctx, "rid")
	require.ErrorIs(t, err, reconcile.ErrLeaseHeld)
}

// TestParseAcquireResult covers the three error paths and the happy path of the
// parseAcquireResult helper (F7 — test coverage for the newly-guarded nil epoch case).
func TestParseAcquireResult(t *testing.T) {
	t.Parallel()
	t.Run("error_wrong_len", func(t *testing.T) {
		_, _, err := parseAcquireResult([]any{int64(1)})
		require.Error(t, err, "len!=2 must error")
	})
	t.Run("error_flag_not_int64", func(t *testing.T) {
		_, _, err := parseAcquireResult([]any{"bad", int64(1)})
		require.Error(t, err, "non-int64 flag must error")
	})
	t.Run("error_epoch_not_int64", func(t *testing.T) {
		_, _, err := parseAcquireResult([]any{int64(1), "bad"})
		require.Error(t, err, "non-int64 epoch must error")
	})
	t.Run("error_epoch_negative", func(t *testing.T) {
		_, _, err := parseAcquireResult([]any{int64(1), int64(-1)})
		require.Error(t, err, "negative epoch must error")
	})
	t.Run("happy_path_acquired", func(t *testing.T) {
		acquired, epoch, err := parseAcquireResult([]any{int64(1), int64(5)})
		require.NoError(t, err)
		require.True(t, acquired)
		require.Equal(t, uint64(5), epoch)
	})
	t.Run("happy_path_not_acquired", func(t *testing.T) {
		acquired, epoch, err := parseAcquireResult([]any{int64(0), int64(0)})
		require.NoError(t, err)
		require.False(t, acquired)
		require.Equal(t, uint64(0), epoch)
	})
}

// TestReconcileElector_AcquireScriptContent golden-locks the acquire Lua so an
// accidental edit (which the mock would not catch) fails at unit-test time.
func TestReconcileElector_AcquireScriptContent(t *testing.T) {
	want := "\nlocal cur = redis.call(\"GET\", KEYS[1])\n" +
		"if cur == false then\n" +
		"    redis.call(\"SET\", KEYS[1], ARGV[1], \"PX\", ARGV[2])\n" +
		"    local e = redis.call(\"INCR\", KEYS[2])\n" +
		"    redis.call(\"EXPIRE\", KEYS[2], ARGV[3])\n" +
		"    return {1, e}\n" +
		"elseif cur == ARGV[1] then\n" +
		"    redis.call(\"PEXPIRE\", KEYS[1], ARGV[2])\n" +
		"    local e = redis.call(\"GET\", KEYS[2])\n" +
		"    if e == false then e = 0 end\n" +
		"    redis.call(\"EXPIRE\", KEYS[2], ARGV[3])\n" +
		"    return {1, tonumber(e)}\n" +
		"else\n" +
		"    return {0, 0}\n" +
		"end\n"
	assert.Equal(t, want, reconcileAcquireScript, "reconcileAcquireScript must not be modified without updating the golden")
}

// TestReconcileElector_RenewScriptContent golden-locks the renew Lua (which refreshes
// BOTH the holder and epoch key TTL — the F1 fix the mock cannot otherwise catch).
func TestReconcileElector_RenewScriptContent(t *testing.T) {
	want := "\nif redis.call(\"GET\", KEYS[1]) == ARGV[1] then\n" +
		"    redis.call(\"PEXPIRE\", KEYS[1], ARGV[2])\n" +
		"    redis.call(\"EXPIRE\", KEYS[2], ARGV[3])\n" +
		"    return 1\n" +
		"else\n" +
		"    return 0\n" +
		"end\n"
	assert.Equal(t, want, reconcileRenewScript, "reconcileRenewScript must not be modified without updating the golden")
}
