package redis

import (
	"context"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	idemhttp "github.com/ghbvf/gocell/runtime/http/idempotency"
)

// =============================================================================
// Constructor tests
// =============================================================================

func TestNewHTTPIdempotencyStore_RejectsNilClient(t *testing.T) {
	store, err := NewHTTPIdempotencyStore(nil, testNamespace)

	require.Error(t, err)
	assert.Nil(t, store)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisConnect, ec.Code)
}

func TestNewHTTPIdempotencyStore_RejectsInvalidNamespace(t *testing.T) {
	store, err := NewHTTPIdempotencyStore(nil, KeyNamespace(""))

	require.Error(t, err)
	assert.Nil(t, store)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
}

func TestNewHTTPIdempotencyStoreFromCmdable_RejectsNilCmdable(t *testing.T) {
	store, err := newHTTPIdempotencyStoreFromCmdable(nil, testNamespace)

	require.Error(t, err)
	assert.Nil(t, store)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterRedisConnect, ec.Code)
}

func TestNewHTTPIdempotencyStoreFromCmdable_RejectsInvalidNamespace(t *testing.T) {
	store, err := newHTTPIdempotencyStoreFromCmdable(newHTTPClaimerMock(), KeyNamespace(""))

	require.Error(t, err)
	assert.Nil(t, store)
}

// =============================================================================
// Claim → Acquired path
// =============================================================================

func TestHTTPIdempotencyStore_Claim_Acquired(t *testing.T) {
	mock := newHTTPClaimerMock()
	store := mustNewHTTPIdempotencyStoreFromCmdable(t, mock)
	ctx := context.Background()

	state, rec, receipt, err := store.Claim(ctx, "testns", "key:001", testtime.D5min)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ClaimAcquired, state)
	assert.Nil(t, rec, "acquired should have nil RecordedResponse")
	assert.NotNil(t, receipt)

	// Verify lease key is present.
	mock.mu.Lock()
	_, hasLease := mock.store["testns:{key:001}:lease"]
	mock.mu.Unlock()
	assert.True(t, hasLease, "lease key should exist after Claim Acquired")
}

// =============================================================================
// Record → second Claim returns Done with replayed response
// =============================================================================

func TestHTTPIdempotencyStore_Record_ThenClaim_Done(t *testing.T) {
	mock := newHTTPClaimerMock()
	store := mustNewHTTPIdempotencyStoreFromCmdable(t, mock)
	ctx := context.Background()

	// First claim acquires the lease.
	state, _, receipt, err := store.Claim(ctx, "testns", "key:002", testtime.D5min)
	require.NoError(t, err)
	require.Equal(t, idempotency.ClaimAcquired, state)
	require.NotNil(t, receipt)

	// Build a recorded response to store.
	resp := buildTestRecordedResponse(t)

	// Record stores the response blob and commits the lease.
	err = receipt.Record(ctx, resp, testtime.D24h)
	require.NoError(t, err)

	// Verify resp key is now present, lease key is gone.
	mock.mu.Lock()
	_, hasLease := mock.store["testns:{key:002}:lease"]
	_, hasResp := mock.store["testns:{key:002}:resp"]
	mock.mu.Unlock()
	assert.False(t, hasLease, "lease key should be deleted after Record")
	assert.True(t, hasResp, "resp key should exist after Record")

	// Second claim returns Done with the replayed response.
	state2, rec2, _, err2 := store.Claim(ctx, "testns", "key:002", testtime.D5min)
	require.NoError(t, err2)
	assert.Equal(t, idempotency.ClaimDone, state2)
	require.NotNil(t, rec2, "ClaimDone should return the recorded response")
	assert.Equal(t, resp.Status(), rec2.Status())
	assert.Equal(t, resp.Body(), rec2.Body())
}

// =============================================================================
// Claim Busy when lease held
// =============================================================================

func TestHTTPIdempotencyStore_Claim_Busy(t *testing.T) {
	mock := newHTTPClaimerMock()
	ctx := context.Background()

	// Pre-set the lease to simulate another consumer.
	mock.mu.Lock()
	mock.store["testns:{key:003}:lease"] = mockEntry{
		value:  "other-token",
		expiry: time.Now().Add(testtime.D5min),
	}
	mock.mu.Unlock()

	store := mustNewHTTPIdempotencyStoreFromCmdable(t, mock)
	state, rec, _, err := store.Claim(ctx, "testns", "key:003", testtime.D5min)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ClaimBusy, state)
	assert.Nil(t, rec, "ClaimBusy should have nil RecordedResponse")
}

// =============================================================================
// Release path
// =============================================================================

func TestHTTPIdempotencyStore_Release(t *testing.T) {
	mock := newHTTPClaimerMock()
	store := mustNewHTTPIdempotencyStoreFromCmdable(t, mock)
	ctx := context.Background()

	state, _, receipt, err := store.Claim(ctx, "testns", "key:004", testtime.D5min)
	require.NoError(t, err)
	require.Equal(t, idempotency.ClaimAcquired, state)

	err = receipt.Release(ctx)
	require.NoError(t, err)

	mock.mu.Lock()
	_, hasLease := mock.store["testns:{key:004}:lease"]
	_, hasResp := mock.store["testns:{key:004}:resp"]
	mock.mu.Unlock()
	assert.False(t, hasLease, "lease key should be deleted after Release")
	assert.False(t, hasResp, "resp key should NOT exist after Release")
}

// =============================================================================
// Stale-token Record
// =============================================================================

func TestHTTPIdempotencyStore_Record_StaleToken(t *testing.T) {
	mock := newHTTPClaimerMock()
	store := mustNewHTTPIdempotencyStoreFromCmdable(t, mock)
	ctx := context.Background()

	state, _, receipt, err := store.Claim(ctx, "testns", "key:005", testtime.D5min)
	require.NoError(t, err)
	require.Equal(t, idempotency.ClaimAcquired, state)

	// Simulate lease expiry by deleting the lease key.
	mock.mu.Lock()
	delete(mock.store, "testns:{key:005}:lease")
	mock.mu.Unlock()

	resp := buildTestRecordedResponse(t)
	err = receipt.Record(ctx, resp, testtime.D24h)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stale lease")
}

// =============================================================================
// Stale-token Release
// =============================================================================

func TestHTTPIdempotencyStore_Release_StaleToken(t *testing.T) {
	mock := newHTTPClaimerMock()
	store := mustNewHTTPIdempotencyStoreFromCmdable(t, mock)
	ctx := context.Background()

	state, _, receipt, err := store.Claim(ctx, "testns", "key:006", testtime.D5min)
	require.NoError(t, err)
	require.Equal(t, idempotency.ClaimAcquired, state)

	// Simulate lease expiry.
	mock.mu.Lock()
	delete(mock.store, "testns:{key:006}:lease")
	mock.mu.Unlock()

	err = receipt.Release(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "stale lease")
}

// =============================================================================
// Double Record / double Release idempotency
// =============================================================================

func TestHTTPIdempotencyStore_DoubleRecord_Idempotent(t *testing.T) {
	mock := newHTTPClaimerMock()
	store := mustNewHTTPIdempotencyStoreFromCmdable(t, mock)
	ctx := context.Background()

	_, _, receipt, err := store.Claim(ctx, "testns", "key:007", testtime.D5min)
	require.NoError(t, err)

	resp := buildTestRecordedResponse(t)
	require.NoError(t, receipt.Record(ctx, resp, testtime.D24h))
	require.NoError(t, receipt.Record(ctx, resp, testtime.D24h), "second Record should be no-op")
}

func TestHTTPIdempotencyStore_DoubleRelease_Idempotent(t *testing.T) {
	mock := newHTTPClaimerMock()
	store := mustNewHTTPIdempotencyStoreFromCmdable(t, mock)
	ctx := context.Background()

	_, _, receipt, err := store.Claim(ctx, "testns", "key:008", testtime.D5min)
	require.NoError(t, err)

	require.NoError(t, receipt.Release(ctx))
	require.NoError(t, receipt.Release(ctx), "second Release should be no-op")
}

// =============================================================================
// Eval error propagation
// =============================================================================

func TestHTTPIdempotencyStore_Claim_EvalError(t *testing.T) {
	mock := newHTTPClaimerMock()
	mock.evalErr = errMock
	store := mustNewHTTPIdempotencyStoreFromCmdable(t, mock)
	ctx := context.Background()

	state, rec, receipt, err := store.Claim(ctx, "testns", "key:009", testtime.D5min)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ERR_ADAPTER_REDIS_SET")
	assert.Equal(t, idempotency.ClaimState(0), state)
	assert.Nil(t, rec)
	assert.Nil(t, receipt)
}

// =============================================================================
// Via public constructor (integration smoke)
// =============================================================================

func TestNewHTTPIdempotencyStore_ViaClientConstructor(t *testing.T) {
	mock := newHTTPClaimerMock()
	client := newClientFromCmdable(mock, Config{})
	store, err := NewHTTPIdempotencyStore(client, testNamespace)
	require.NoError(t, err)

	ctx := context.Background()
	state, _, _, err := store.Claim(ctx, "testns", "key:client:001", testtime.D5min)
	require.NoError(t, err)
	assert.Equal(t, idempotency.ClaimAcquired, state)
}

// =============================================================================
// Helpers
// =============================================================================

// buildTestRecordedResponse creates a minimal RecordedResponse by round-trip
// through UnmarshalRecordedResponse since the constructor is package-internal.
func buildTestRecordedResponse(t *testing.T) *idemhttp.RecordedResponse {
	t.Helper()
	raw := []byte(`{"status":200,"body":"dGVzdA==","header":{"Content-Type":["application/json"]},"recordedAt":"2024-01-01T00:00:00Z"}`)
	r, err := idemhttp.UnmarshalRecordedResponse(raw)
	require.NoError(t, err)
	return &r
}

// mustNewHTTPIdempotencyStoreFromCmdable creates an HTTPIdempotencyStore via
// the internal test seam.
func mustNewHTTPIdempotencyStoreFromCmdable(t *testing.T, rdb cmdable) *HTTPIdempotencyStore {
	t.Helper()
	s, err := newHTTPIdempotencyStoreFromCmdable(rdb, testNamespace)
	if err != nil {
		t.Fatalf("newHTTPIdempotencyStoreFromCmdable: %v", err)
	}
	return s
}

// httpClaimerMockCmdable extends claimerMockCmdable with Eval behavior for
// the HTTP idempotency Lua scripts (claimResp, record, release).
//
// Dispatch by KEYS ordering (suffix-matching convention, consistent with the
// existing ":done" / ":lease" dispatch in claimerMockCmdable):
//
//   - claimRespScript:  KEYS=[resp_key, lease_key], 2 args → keys[0] ends in ":resp"
//   - recordScript:     KEYS=[lease_key, resp_key], 3 args → keys[0] ends in ":lease"
//   - releaseScript:    KEYS=[lease_key],            1 arg  → (inherited, single key)
type httpClaimerMockCmdable struct {
	claimerMockCmdable
}

func newHTTPClaimerMock() *httpClaimerMockCmdable {
	return &httpClaimerMockCmdable{
		claimerMockCmdable: claimerMockCmdable{
			mockCmdable: mockCmdable{
				store: make(map[string]mockEntry),
			},
		},
	}
}

// evalClaimResp simulates claimRespScript:
// KEYS=[resp_key, lease_key], ARGV=[token, leaseMs].
// Returns int64(2) + blob for Done, int64(1) for Acquired, int64(0) for Busy.
// Caller MUST hold m.mu.
func (m *httpClaimerMockCmdable) evalClaimResp(cmd *goredis.Cmd, keys []string, args []any) {
	respKey, leaseKey := keys[0], keys[1]
	token := toString(args[0])
	leaseMs := toInt64(args[1])

	// Check resp key first (Done path).
	if entry, ok := m.store[respKey]; ok {
		if entry.expiry.IsZero() || time.Now().Before(entry.expiry) {
			cmd.SetVal([]any{int64(2), entry.value})
			return
		}
		delete(m.store, respKey) // expired
	}

	// Check lease key (Busy path).
	if entry, ok := m.store[leaseKey]; ok {
		if entry.expiry.IsZero() || time.Now().Before(entry.expiry) {
			cmd.SetVal(int64(0)) // ClaimBusy = 0
			return
		}
		delete(m.store, leaseKey) // expired
	}

	// Acquire: SET lease NX.
	m.store[leaseKey] = mockEntry{
		value:  token,
		expiry: time.Now().Add(time.Duration(leaseMs) * time.Millisecond),
	}
	cmd.SetVal(int64(1)) // ClaimAcquired = 1
}

// evalRecord simulates recordScript:
// KEYS=[lease_key, resp_key], ARGV=[token, blob, doneTTLMs].
// Returns 1 on success, 0 on token mismatch (stale lease).
// Caller MUST hold m.mu.
func (m *httpClaimerMockCmdable) evalRecord(cmd *goredis.Cmd, keys []string, args []any) {
	leaseKey, respKey := keys[0], keys[1]
	token := toString(args[0])
	blob := toString(args[1])
	doneTTLMs := toInt64(args[2])

	if entry, ok := m.store[leaseKey]; ok && entry.value == token {
		delete(m.store, leaseKey)
		m.store[respKey] = mockEntry{
			value:  blob,
			expiry: time.Now().Add(time.Duration(doneTTLMs) * time.Millisecond),
		}
		cmd.SetVal(int64(1))
	} else {
		cmd.SetVal(int64(0))
	}
}

// Eval overrides the embedded claimerMockCmdable to handle the HTTP
// idempotency Lua scripts. Dispatch is by KEYS count + suffix matching:
//
//   - 2 keys, 2 args, keys[0] ends in ":resp"   → claimRespScript
//   - 2 keys, 3 args, keys[0] ends in ":lease"  → recordScript
//   - 1 key,  1 arg                              → releaseScript
func (m *httpClaimerMockCmdable) Eval(_ context.Context, _ string, keys []string, args ...any) *goredis.Cmd {
	cmd := goredis.NewCmd(context.Background())
	if m.evalErr != nil {
		cmd.SetErr(m.evalErr)
		return cmd
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	switch {
	// claimRespScript: 2 keys, 2 args, first key ends in ":resp"
	case len(keys) == 2 && len(args) == 2 && strings.HasSuffix(keys[0], ":resp"):
		m.evalClaimResp(cmd, keys, args)
	// recordScript: 2 keys, 3 args, first key ends in ":lease"
	case len(keys) == 2 && len(args) == 3 && strings.HasSuffix(keys[0], ":lease"):
		m.evalRecord(cmd, keys, args)
	// releaseScript: 1 key, 1 arg (inherited behavior)
	case len(keys) == 1 && len(args) == 1:
		m.evalRelease(cmd, keys, args)
	default:
		cmd.SetVal(int64(0))
	}
	return cmd
}
