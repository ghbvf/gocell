package redis

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/reconcile"
	"github.com/ghbvf/gocell/kernel/reconcile/reconciletest"
	"github.com/ghbvf/gocell/pkg/errcode"
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
	holderKey, epochKey := keys[0], keys[1]
	holderID := toString(args[0])
	ttl := time.Duration(toInt64(args[1])) * time.Millisecond
	if cur, live := m.liveValue(holderKey); live && cur == holderID {
		if _, ok := m.store[epochKey]; !ok { // epoch key vanished mid-term → fail-closed
			cmd.SetErr(errors.New("reconcile epoch key missing on renew"))
			return
		}
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
		if _, ok := m.store[epochKey]; !ok { // epoch key vanished while still holding → fail-closed
			cmd.SetErr(errors.New("reconcile epoch key missing on same-holder re-acquire"))
			return
		}
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

// reconcileTestLeaseTTL is the lease duration used across the reconcile elector
// unit + integration tests (TEST-TIME-LITERAL-01: literal lives in a package-level
// const, call sites reference it). Declared in this untagged file so the
// integration-tagged file can share it without a duplicate declaration.
const reconcileTestLeaseTTL = 30 * time.Second

// mustLease builds the shared test LeaseTTL from the package-level literal const
// (electors now take a validated reconcile.LeaseTTL, not a raw time.Duration —
// sub-ms truncation is foreclosed at NewLeaseTTL, see kernel/reconcile/leasettl.go).
func mustLease(t *testing.T) reconcile.LeaseTTL {
	t.Helper()
	l, err := reconcile.NewLeaseTTL(reconcileTestLeaseTTL)
	require.NoError(t, err)
	return l
}

// mustElector builds a mock-backed elector. holderID is minted internally (each
// call → a distinct UUID), so two electors over the same mock contend as distinct
// holders without a caller-supplied label.
func mustElector(t *testing.T, rdb cmdable) *RedisReconcileElector {
	t.Helper()
	e, err := newReconcileElectorFromCmdable(rdb, "reconcile", mustLease(t), clock.Real())
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

// TestReconcileElector_ConstructorValidation covers the constructor guard
// branches: nil client, nil cmdable, invalid namespace, and the unconstructed
// zero-value lease each fail-fast before an elector is built. (Sub-ms lease
// rejection lives upstream in NewLeaseTTL — TestNewLeaseTTL — because the elector
// no longer accepts a raw time.Duration; the residual zero-value LeaseTTL{} is the
// only invalid lease a caller can still pass here.)
func TestReconcileElector_ConstructorValidation(t *testing.T) {
	t.Run("nil_client", func(t *testing.T) {
		_, err := NewRedisReconcileElector(nil, "reconcile", mustLease(t), clock.Real())
		require.Error(t, err)
	})
	t.Run("nil_cmdable", func(t *testing.T) {
		_, err := newReconcileElectorFromCmdable(nil, "reconcile", mustLease(t), clock.Real())
		require.Error(t, err)
	})
	t.Run("invalid_namespace", func(t *testing.T) {
		// uppercase is rejected by KeyNamespace.Validate
		_, err := newReconcileElectorFromCmdable(newReconcileMock(), "BadNS", mustLease(t), clock.Real())
		require.Error(t, err)
	})
	t.Run("zero_value_lease", func(t *testing.T) {
		// the unconstructed LeaseTTL{} (ms==0) must fail-fast, not yield a 0ms lease
		_, err := newReconcileElectorFromCmdable(newReconcileMock(), "reconcile", reconcile.LeaseTTL{}, clock.Real())
		require.Error(t, err)
		var ec *errcode.Error
		require.ErrorAs(t, err, &ec)
		require.Equal(t, errcode.ErrCellInvalidConfig, ec.Code,
			"a zero-value lease is a config mistake, routable separately from a redis connect failure")
	})
}

// TestReconcileElector_EvalErrorPaths covers the I/O-error branches of
// AcquireLease / RenewLease / ReleaseLease: a backend Eval failure surfaces as a
// wrapped (non-sentinel) error from each method.
func TestReconcileElector_EvalErrorPaths(t *testing.T) {
	ctx := context.Background()
	boom := errors.New("redis down")

	t.Run("acquire", func(t *testing.T) {
		mock := newReconcileMock()
		mock.evalErr = boom
		e := mustElector(t, mock)
		_, err := e.AcquireLease(ctx, "rid")
		require.Error(t, err)
		require.NotErrorIs(t, err, reconcile.ErrLeaseHeld, "I/O fault is not contention")
	})
	t.Run("renew", func(t *testing.T) {
		mock := newReconcileMock()
		mock.evalErr = boom
		e := mustElector(t, mock)
		err := e.RenewLease(ctx, reconcile.LeaseToken{ReconcilerID: "rid", HolderID: e.holderID})
		require.Error(t, err)
		require.NotErrorIs(t, err, reconcile.ErrReconcileLeaseLost, "I/O fault is not a clean lease-lost")
	})
	t.Run("release", func(t *testing.T) {
		mock := newReconcileMock()
		mock.evalErr = boom
		e := mustElector(t, mock)
		err := e.ReleaseLease(ctx, reconcile.LeaseToken{ReconcilerID: "rid", HolderID: e.holderID})
		require.Error(t, err)
	})
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
		"    local e = redis.call(\"GET\", KEYS[2])\n" +
		"    if e == false then return redis.error_reply(\"reconcile epoch key missing on same-holder re-acquire\") end\n" +
		"    redis.call(\"PEXPIRE\", KEYS[1], ARGV[2])\n" +
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
		"    if redis.call(\"GET\", KEYS[2]) == false then return redis.error_reply(\"reconcile epoch key missing on renew\") end\n" +
		"    redis.call(\"PEXPIRE\", KEYS[1], ARGV[2])\n" +
		"    redis.call(\"EXPIRE\", KEYS[2], ARGV[3])\n" +
		"    return 1\n" +
		"else\n" +
		"    return 0\n" +
		"end\n"
	assert.Equal(t, want, reconcileRenewScript, "reconcileRenewScript must not be modified without updating the golden")
}

// TestReconcileElector_EpochKeyMissing_FailClosed verifies that a live holder whose
// epoch key has vanished (Redis eviction / operational deletion) gets a fail-closed
// error on BOTH live-holder paths — same-holder re-acquire AND renew — never a
// silently rolled-back epoch 0 (PR-A6 round-3 review). The free-holder
// rebuild-from-1 residual is an inherent Redis limitation (a genuine first-ever
// acquire is indistinguishable from a post-eviction takeover) documented in
// reconcileAcquireScript's godoc; the durable monotonic SoR is the Postgres adapter.
func TestReconcileElector_EpochKeyMissing_FailClosed(t *testing.T) {
	ctx := context.Background()

	// deleteEpochKeys drops every epoch key (suffix ":epoch") while leaving the
	// holder key (":lease") live — i.e. the holder still owns the lease but its
	// monotonic token has vanished from Redis.
	deleteEpochKeys := func(m *reconcileMockCmdable) {
		m.mu.Lock()
		defer m.mu.Unlock()
		for k := range m.store {
			if strings.HasSuffix(k, ":epoch") {
				delete(m.store, k)
			}
		}
	}

	t.Run("same_holder_reacquire", func(t *testing.T) {
		mock := newReconcileMock()
		e := mustElector(t, mock)
		tok, err := e.AcquireLease(ctx, "rid")
		require.NoError(t, err)
		require.Equal(t, uint64(1), tok.Epoch)

		deleteEpochKeys(mock)

		_, err = e.AcquireLease(ctx, "rid")
		require.Error(t, err, "same-holder re-acquire with missing epoch key must fail-closed")
		require.NotErrorIs(t, err, reconcile.ErrLeaseHeld,
			"missing epoch is a fencing-integrity fault, not contention")
	})

	t.Run("renew", func(t *testing.T) {
		mock := newReconcileMock()
		e := mustElector(t, mock)
		tok, err := e.AcquireLease(ctx, "rid")
		require.NoError(t, err)

		deleteEpochKeys(mock)

		err = e.RenewLease(ctx, tok)
		require.Error(t, err, "renew with missing epoch key must fail-closed")
		require.NotErrorIs(t, err, reconcile.ErrReconcileLeaseLost,
			"missing epoch is a fencing-integrity fault, not a normal handoff")
	})
}
