package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	goredis "github.com/redis/go-redis/v9"
)

// ---------------------------------------------------------------------------
// IdempotencyChecker — legacy (Deprecated, retained for Checker interface)
// ---------------------------------------------------------------------------

// Compile-time interface check.
var _ idempotency.Checker = (*IdempotencyChecker)(nil)

// Deprecated: IdempotencyChecker implements the legacy idempotency.Checker.
// New code should use IdempotencyClaimer (two-phase Claim/Commit/Release).
type IdempotencyChecker struct {
	rdb cmdable
}

// NewIdempotencyChecker creates a new IdempotencyChecker using the given Client.
func NewIdempotencyChecker(client *Client) *IdempotencyChecker {
	return &IdempotencyChecker{rdb: client.cmdable()}
}

// newIdempotencyCheckerFromCmdable creates an IdempotencyChecker with a
// pre-built cmdable for testing.
func newIdempotencyCheckerFromCmdable(rdb cmdable) *IdempotencyChecker {
	return &IdempotencyChecker{rdb: rdb}
}

// IsProcessed returns true if the given idempotency key has already been
// marked as processed. It returns false and nil error for keys that do not
// exist (i.e., not yet processed).
func (ic *IdempotencyChecker) IsProcessed(ctx context.Context, key string) (bool, error) {
	val, err := ic.rdb.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, goredis.Nil) {
			return false, nil
		}
		return false, errcode.Wrap(ErrAdapterRedisGet,
			fmt.Sprintf("redis: idempotency check failed (key=%s)", key), err)
	}
	return val == "1", nil
}

// MarkProcessed atomically marks the idempotency key as processed using
// SET NX with the given TTL. If the key already exists (already processed),
// the operation is a no-op and returns nil.
// If ttl <= 0, idempotency.DefaultTTL (24h) is used to prevent permanent keys.
func (ic *IdempotencyChecker) MarkProcessed(ctx context.Context, key string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = idempotency.DefaultTTL
	}
	_, err := ic.rdb.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		return errcode.Wrap(ErrAdapterRedisSet,
			fmt.Sprintf("redis: idempotency mark failed (key=%s)", key), err)
	}
	return nil
}

// TryProcess atomically checks whether key has been processed and marks it if not.
// Returns true if the caller should process (key was not previously seen).
// Returns false if already processed (another consumer got there first).
// Uses Redis SetNX which is inherently atomic, eliminating the TOCTOU race.
// If ttl <= 0, idempotency.DefaultTTL (24h) is used to prevent permanent keys.
func (ic *IdempotencyChecker) TryProcess(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	if ttl <= 0 {
		ttl = idempotency.DefaultTTL
	}
	set, err := ic.rdb.SetNX(ctx, key, "1", ttl).Result()
	if err != nil {
		return false, errcode.Wrap(ErrAdapterRedisSet,
			fmt.Sprintf("redis: idempotency try-process failed (key=%s)", key), err)
	}
	return set, nil
}

// Release removes the idempotency key so a redelivered message can be processed
// again. This must be called when a message is requeued after TryProcess already
// claimed the key (e.g., DLQ publish failure, shutdown).
func (ic *IdempotencyChecker) Release(ctx context.Context, key string) error {
	_, err := ic.rdb.Del(ctx, key).Result()
	if err != nil {
		return errcode.Wrap(ErrAdapterRedisDelete,
			fmt.Sprintf("redis: idempotency release failed (key=%s)", key), err)
	}
	return nil
}

// ---------------------------------------------------------------------------
// IdempotencyClaimer — two-phase model (Solution B)
// ---------------------------------------------------------------------------

// Compile-time interface check.
var _ idempotency.Claimer = (*IdempotencyClaimer)(nil)

// IdempotencyClaimer implements idempotency.Claimer using a dual-key Lua
// script model:
//
//   - lease:{key} — SET NX with leaseTTL, value = random token. Indicates "processing".
//   - done:{key}  — SET with doneTTL, value = "1". Indicates "completed".
//
// Claim checks done first (ClaimDone), then attempts lease (ClaimAcquired or ClaimBusy).
// Commit sets done + deletes lease. Release deletes lease (token-guarded).
type IdempotencyClaimer struct {
	rdb cmdable
}

// NewIdempotencyClaimer creates an IdempotencyClaimer using the given Client.
func NewIdempotencyClaimer(client *Client) *IdempotencyClaimer {
	return &IdempotencyClaimer{rdb: client.cmdable()}
}

// newIdempotencyClaimerFromCmdable creates an IdempotencyClaimer with a
// pre-built cmdable for testing.
func newIdempotencyClaimerFromCmdable(rdb cmdable) *IdempotencyClaimer {
	return &IdempotencyClaimer{rdb: rdb}
}

// claimScript is the Lua script for atomic Claim:
//
//	KEYS[1] = done:{key}
//	KEYS[2] = lease:{key}
//	ARGV[1] = token
//	ARGV[2] = leaseTTL (seconds)
//
// Returns:
//
//	0 = ClaimDone     (done key exists)
//	1 = ClaimAcquired (lease key set successfully)
//	2 = ClaimBusy     (lease key already held by another consumer)
const claimScript = `
local done = redis.call('EXISTS', KEYS[1])
if done == 1 then
  return 0
end
local ok = redis.call('SET', KEYS[2], ARGV[1], 'NX', 'EX', ARGV[2])
if ok then
  return 1
end
return 2
`

// commitScript: atomic Commit (token-guarded):
//
//	KEYS[1] = lease:{key}
//	KEYS[2] = done:{key}
//	ARGV[1] = token
//	ARGV[2] = doneTTL (seconds)
//
// Returns 1 on success, 0 if token mismatch (stale lease).
const commitScript = `
local val = redis.call('GET', KEYS[1])
if val == ARGV[1] then
  redis.call('DEL', KEYS[1])
  redis.call('SET', KEYS[2], '1', 'EX', ARGV[2])
  return 1
end
return 0
`

// releaseScript: atomic Release (token-guarded):
//
//	KEYS[1] = lease:{key}
//	ARGV[1] = token
//
// Returns 1 on success, 0 if token mismatch.
const releaseScript = `
local val = redis.call('GET', KEYS[1])
if val == ARGV[1] then
  redis.call('DEL', KEYS[1])
  return 1
end
return 0
`

// Claim attempts to acquire a processing lease for the given key.
func (c *IdempotencyClaimer) Claim(ctx context.Context, key string, leaseTTL, doneTTL time.Duration) (idempotency.ClaimState, outbox.Receipt, error) {
	if leaseTTL <= 0 {
		leaseTTL = idempotency.DefaultLeaseTTL
	}
	if doneTTL <= 0 {
		doneTTL = idempotency.DefaultTTL
	}

	token, err := claimToken()
	if err != nil {
		return 0, nil, errcode.Wrap(ErrAdapterRedisSet,
			fmt.Sprintf("redis: idempotency claim token generation failed (key=%s)", key), err)
	}

	leaseKey := "lease:" + key
	doneKey := "done:" + key
	leaseSec := int64(leaseTTL.Seconds())
	if leaseSec < 1 {
		leaseSec = 1
	}

	res, err := c.rdb.Eval(ctx, claimScript, []string{doneKey, leaseKey}, token, leaseSec).Result()
	if err != nil {
		return 0, nil, errcode.Wrap(ErrAdapterRedisSet,
			fmt.Sprintf("redis: idempotency claim failed (key=%s)", key), err)
	}

	code, ok := res.(int64)
	if !ok {
		return 0, nil, errcode.New(ErrAdapterRedisGet,
			fmt.Sprintf("redis: idempotency claim unexpected result type (key=%s)", key))
	}

	switch code {
	case 0:
		return idempotency.ClaimDone, nil, nil
	case 1:
		r := &redisReceipt{
			rdb:      c.rdb,
			leaseKey: leaseKey,
			doneKey:  doneKey,
			token:    token,
			doneTTL:  doneTTL,
		}
		return idempotency.ClaimAcquired, r, nil
	default:
		return idempotency.ClaimBusy, nil, nil
	}
}

// redisReceipt implements outbox.Receipt for the two-phase idempotency model.
type redisReceipt struct {
	rdb      cmdable
	leaseKey string
	doneKey  string
	token    string
	doneTTL  time.Duration
}

// Compile-time interface check.
var _ outbox.Receipt = (*redisReceipt)(nil)

// Commit marks the key as permanently done and removes the lease.
func (r *redisReceipt) Commit(ctx context.Context) error {
	doneSec := int64(r.doneTTL.Seconds())
	if doneSec < 1 {
		doneSec = 1
	}
	res, err := r.rdb.Eval(ctx, commitScript, []string{r.leaseKey, r.doneKey}, r.token, doneSec).Result()
	if err != nil {
		return errcode.Wrap(ErrAdapterRedisSet,
			fmt.Sprintf("redis: idempotency commit failed (lease=%s)", r.leaseKey), err)
	}
	code, ok := res.(int64)
	if !ok || code == 0 {
		return errcode.New(ErrAdapterRedisSet,
			fmt.Sprintf("redis: idempotency commit token mismatch (stale lease, key=%s)", r.leaseKey))
	}
	return nil
}

// Release removes the processing lease so a redelivered message can re-enter.
func (r *redisReceipt) Release(ctx context.Context) error {
	res, err := r.rdb.Eval(ctx, releaseScript, []string{r.leaseKey}, r.token).Result()
	if err != nil {
		return errcode.Wrap(ErrAdapterRedisDelete,
			fmt.Sprintf("redis: idempotency release failed (lease=%s)", r.leaseKey), err)
	}
	code, ok := res.(int64)
	if !ok || code == 0 {
		return errcode.New(ErrAdapterRedisDelete,
			fmt.Sprintf("redis: idempotency release token mismatch (stale lease, key=%s)", r.leaseKey))
	}
	return nil
}

// claimToken generates a 16-byte hex-encoded token for lease ownership.
func claimToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
