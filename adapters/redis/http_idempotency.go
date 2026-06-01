package redis

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/errcode"
	idemhttp "github.com/ghbvf/gocell/runtime/http/idempotency"
)

// ---------------------------------------------------------------------------
// HTTPIdempotencyStore — dual-key Lua script model for HTTP idempotency
// ---------------------------------------------------------------------------

// Compile-time interface check.
var _ idemhttp.Store = (*HTTPIdempotencyStore)(nil)

// HTTPIdempotencyStore implements idemhttp.Store using a dual-key Lua script
// model that stores the full HTTP response blob for replay.
//
// Consistency: L1 (LocalTx) — each Lua script executes atomically within Redis.
//
//   - <ns>:{key}:lease — SET NX with leaseTTL, value = random token. Indicates "processing".
//   - <ns>:{key}:resp  — SET with doneTTL, value = MarshalRecordedResponse blob. Indicates "completed".
//
// Cluster: keys are wrapped in a Redis Cluster hashtag so CRC16 hashes only
// the business-key portion; lease and resp keys colocate on the same slot,
// keeping multi-KEY EVAL safe under Cluster mode.
// The KeyNamespace prefix sits outside the hashtag so slot colocality is
// preserved regardless of namespace value.
//
// Claim checks resp first (ClaimDone+replay), then attempts lease
// (ClaimAcquired or ClaimBusy). Record sets resp + deletes lease.
// Release deletes lease (token-guarded).
type HTTPIdempotencyStore struct {
	rdb cmdable
	ns  KeyNamespace
}

// NewHTTPIdempotencyStore creates an HTTPIdempotencyStore using the given
// Client and KeyNamespace. ns is validated up front; nil client and invalid
// namespace produce structured errors so misconfiguration fails-fast at
// composition time.
//
// The construction-time KeyNamespace acts as a wiring-validation sentinel
// (REDIS-KEY-NAMESPACE-01): it must be a valid non-empty, lowercase,
// brace-free string ≤48 chars. It is NOT embedded in runtime Redis keys;
// the actual key prefix is the ns argument passed to Claim at request time
// (the caller TenantID or "_notenant" sentinel when using the standard Middleware).
func NewHTTPIdempotencyStore(client *Client, ns KeyNamespace) (*HTTPIdempotencyStore, error) {
	if err := ns.Validate(); err != nil {
		return nil, err
	}
	if client == nil {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterRedisConnect,
			"redis http idempotency store: client is nil")
	}
	return newHTTPIdempotencyStoreFromCmdable(client.cmdable(), ns)
}

// newHTTPIdempotencyStoreFromCmdable creates an HTTPIdempotencyStore with a
// pre-built cmdable for testing. Re-validates ns and the cmdable so direct
// test callers cannot bypass the public constructor's invariants.
func newHTTPIdempotencyStoreFromCmdable(rdb cmdable, ns KeyNamespace) (*HTTPIdempotencyStore, error) {
	if err := ns.Validate(); err != nil {
		return nil, err
	}
	if rdb == nil {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterRedisConnect,
			"redis http idempotency store: cmdable is nil")
	}
	return &HTTPIdempotencyStore{rdb: rdb, ns: ns}, nil
}

// claimRespScript is the Lua script for atomic Claim. KEYS[1] is the resp-key
// (suffix `:resp`) — the mock dispatch in http_idempotency_test.go recognizes
// the claim script by `strings.HasSuffix(keys[0], ":resp")`, consistent with
// the existing ":done" / ":lease" suffix-matching convention.
//
// KEYS[1] = <ns>:{key}:resp
// KEYS[2] = <ns>:{key}:lease
// ARGV[1] = token
// ARGV[2] = leaseTTL (milliseconds — PX precision)
//
// Returns:
//
//	{1}      = ClaimAcquired (lease set successfully)
//	{0}      = ClaimBusy    (lease already held)
//	{2,blob} = ClaimDone    (resp key exists; blob = stored response)
const claimRespScript = `
local resp = redis.call('GET', KEYS[1])
if resp ~= false then
  return {2, resp}
end
local ok = redis.call('SET', KEYS[2], ARGV[1], 'NX', 'PX', ARGV[2])
if ok then
  return {1}
end
return {0}
`

// recordScript: atomic Record (token-guarded). KEYS[1] is the lease-key —
// see claimRespScript's note above for why claim and record use opposite KEYS order.
//
// KEYS[1] = <ns>:{key}:lease
// KEYS[2] = <ns>:{key}:resp
// ARGV[1] = token
// ARGV[2] = response blob (MarshalRecordedResponse output)
// ARGV[3] = doneTTL (milliseconds — PX precision)
//
// Returns 1 on success, 0 if token mismatch (stale lease).
const httpRecordScript = `
local val = redis.call('GET', KEYS[1])
if val == ARGV[1] then
  redis.call('DEL', KEYS[1])
  redis.call('SET', KEYS[2], ARGV[2], 'PX', ARGV[3])
  return 1
end
return 0
`

// httpReleaseScript: atomic Release (token-guarded):
//
// KEYS[1] = <ns>:{key}:lease
// ARGV[1] = token
//
// Returns 1 on success, 0 if token mismatch.
const httpReleaseScript = `
local val = redis.call('GET', KEYS[1])
if val == ARGV[1] then
  redis.call('DEL', KEYS[1])
  return 1
end
return 0
`

// Claim implements idemhttp.Store. It attempts to acquire a processing lease
// for the given ns+key pair.
//
// The Redis keys are derived as:
//
//	<ns>:{<key>}:lease  and  <ns>:{<key>}:resp
//
// where <ns> is the Claim ns parameter. In the standard Middleware, ns is the
// caller TenantID (or "_notenant" when absent); the interface contract accepts
// any non-empty, brace-free string. <key> is the idempotency key from the
// request header (subject+"\x00"+Idempotency-Key value as composed by Middleware).
//
// The store's construction-time KeyNamespace is validated at construction time
// as a misconfiguration sentinel (REDIS-KEY-NAMESPACE-01); it is NOT embedded
// in the runtime Redis keys — the runtime ns arg is the actual key prefix.
//
// Both ns and key must be non-empty and free of '{'/'}' characters so the
// Redis Cluster hashtag boundary is unambiguous.
func (s *HTTPIdempotencyStore) Claim(
	ctx context.Context, ns, key string, leaseTTL time.Duration,
) (idempotency.ClaimState, *idemhttp.RecordedResponse, idemhttp.Receipt, error) {
	if ns == "" || strings.ContainsAny(ns, "{}") {
		return 0, nil, nil, errcode.New(errcode.KindInternal, ErrAdapterRedisSet,
			"redis: http idempotency ns must be non-empty and free of curly-brace characters",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("ns=%q", ns))))
	}
	if key == "" || strings.ContainsAny(key, "{}") {
		return 0, nil, nil, errcode.New(errcode.KindInternal, ErrAdapterRedisSet,
			"redis: http idempotency key must be non-empty and free of curly-brace characters",
			errcode.WithInternal(errcode.InternalAttr("_", fmt.Sprintf("key=%q", key))))
	}
	if leaseTTL <= 0 {
		leaseTTL = idempotency.DefaultLeaseTTL
	}

	token, err := claimToken()
	if err != nil {
		return 0, nil, nil, errcode.Wrap(errcode.KindInternal, ErrAdapterRedisSet,
			"redis: http idempotency claim token generation failed", err)
	}

	// Derive Redis keys: <ns>:{<key>}:<role>
	// Uses KeyNamespace(ns).applyHashtag so the business key sits inside the
	// hashtag for Redis Cluster slot colocation (lease + resp on same slot).
	respKey := KeyNamespace(ns).applyHashtag(key, "resp")
	leaseKey := KeyNamespace(ns).applyHashtag(key, "lease")
	leaseMs := max(leaseTTL.Milliseconds(), 1)

	res, err := s.rdb.Eval(ctx, claimRespScript, []string{respKey, leaseKey}, token, leaseMs).Result()
	if err != nil {
		return 0, nil, nil, classifyRedisError(err, ErrAdapterRedisSet, "http idempotency claim")
	}

	return s.decodeClaim(res, respKey, leaseKey, token)
}

// decodeClaim decodes the Lua result from claimRespScript.
func (s *HTTPIdempotencyStore) decodeClaim(
	res any, respKey, leaseKey, token string,
) (idempotency.ClaimState, *idemhttp.RecordedResponse, idemhttp.Receipt, error) {
	switch v := res.(type) {
	case int64:
		// {0} = ClaimBusy, {1} = ClaimAcquired (single-element slice decoded as int64 by go-redis).
		// nil rec + nil err is the normal shape here: ClaimState is the discriminator,
		// rec is only non-nil on ClaimDone (the replay branch below).
		if v == 1 {
			r := &httpReceipt{rdb: s.rdb, leaseKey: leaseKey, respKey: respKey, token: token}
			return idempotency.ClaimAcquired, nil, r, nil //nolint:nilnil // ClaimState discriminates; rec nil unless ClaimDone
		}
		return idempotency.ClaimBusy, nil, noopHTTPReceipt{}, nil //nolint:nilnil // ClaimState discriminates; rec nil unless ClaimDone
	case []any:
		// {2, blob} = ClaimDone
		return decodeClaimDone(v)
	default:
		return 0, nil, nil, errcode.New(errcode.KindInternal, ErrAdapterRedisGet,
			"redis: http idempotency claim unexpected result type")
	}
}

// decodeClaimDone decodes the {2, blob} ClaimDone result shape from claimRespScript.
func decodeClaimDone(v []any) (idempotency.ClaimState, *idemhttp.RecordedResponse, idemhttp.Receipt, error) {
	if len(v) >= 2 {
		if code, ok := v[0].(int64); ok && code == 2 {
			if blob, ok2 := v[1].(string); ok2 {
				rec, err := idemhttp.UnmarshalRecordedResponse([]byte(blob))
				if err != nil {
					return 0, nil, nil, errcode.Wrap(errcode.KindInternal, ErrAdapterRedisGet,
						"redis: http idempotency claim replay decode failed", err)
				}
				return idempotency.ClaimDone, &rec, noopHTTPReceipt{}, nil
			}
		}
	}
	return 0, nil, nil, errcode.New(errcode.KindInternal, ErrAdapterRedisGet,
		"redis: http idempotency claim unexpected slice result shape")
}

// ---------------------------------------------------------------------------
// httpReceipt — Receipt for ClaimAcquired
// ---------------------------------------------------------------------------

// httpReceipt implements idemhttp.Receipt for an acquired lease.
type httpReceipt struct {
	rdb      cmdable
	leaseKey string
	respKey  string
	token    string

	mu         sync.Mutex
	recorded   bool
	recordErr  error
	released   bool
	releaseErr error
}

// Compile-time interface check.
var _ idemhttp.Receipt = (*httpReceipt)(nil)

// Record persists resp as the response blob and commits the lease atomically.
// Repeat calls after a successful Record are no-ops (return nil).
// Stale-lease errors are cached (permanent). Redis timeouts are NOT cached,
// allowing retry with a fresh ctx.
func (r *httpReceipt) Record(ctx context.Context, resp *idemhttp.RecordedResponse, doneTTL time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.recorded {
		return r.recordErr
	}
	if doneTTL <= 0 {
		doneTTL = idempotency.DefaultTTL
	}
	blob, err := idemhttp.MarshalRecordedResponse(*resp)
	if err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterRedisSet,
			"redis: http idempotency record marshal failed", err)
	}
	doneTTLMs := max(doneTTL.Milliseconds(), 1)
	res, err := r.rdb.Eval(ctx, httpRecordScript, []string{r.leaseKey, r.respKey}, r.token, string(blob), doneTTLMs).Result()
	if err != nil {
		r.recordErr = classifyRedisError(err, ErrAdapterRedisSet, "http idempotency record")
		return r.recordErr
	}
	code, ok := res.(int64)
	if !ok || code == 0 {
		r.recordErr = errcode.New(errcode.KindInternal, ErrAdapterRedisSet,
			"redis: http idempotency record token mismatch (stale lease)")
		r.recorded = true // stale lease is permanent, don't retry
		return r.recordErr
	}
	r.recordErr = nil
	r.recorded = true
	return nil
}

// Release removes the processing lease without recording a response.
// Repeat calls after a successful Release are no-ops (return nil).
// Stale-lease errors are cached (permanent). Redis timeouts are NOT cached,
// allowing retry with a fresh ctx.
func (r *httpReceipt) Release(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released {
		return r.releaseErr
	}
	res, err := r.rdb.Eval(ctx, httpReleaseScript, []string{r.leaseKey}, r.token).Result()
	if err != nil {
		r.releaseErr = classifyRedisError(err, ErrAdapterRedisDelete, "http idempotency release")
		return r.releaseErr
	}
	code, ok := res.(int64)
	if !ok || code == 0 {
		r.releaseErr = errcode.New(errcode.KindInternal, ErrAdapterRedisDelete,
			"redis: http idempotency release token mismatch (stale lease)")
		r.released = true // stale lease is permanent, don't retry
		return r.releaseErr
	}
	r.releaseErr = nil
	r.released = true
	return nil
}

// ---------------------------------------------------------------------------
// noopHTTPReceipt — Receipt for ClaimDone / ClaimBusy
// ---------------------------------------------------------------------------

// noopHTTPReceipt implements idemhttp.Receipt for non-acquired claim states.
// Both methods return idempotency.ErrNoClaimLease.
type noopHTTPReceipt struct{}

// Compile-time interface check.
var _ idemhttp.Receipt = noopHTTPReceipt{}

func (noopHTTPReceipt) Record(_ context.Context, _ *idemhttp.RecordedResponse, _ time.Duration) error {
	return idempotency.ErrNoClaimLease
}

func (noopHTTPReceipt) Release(_ context.Context) error {
	return idempotency.ErrNoClaimLease
}
