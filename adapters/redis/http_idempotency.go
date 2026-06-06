package redis

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/kernel/idempotency"
	"github.com/ghbvf/gocell/pkg/errcode"
	idemhttp "github.com/ghbvf/gocell/runtime/http/idempotency"
)

// ---------------------------------------------------------------------------
// HTTPIdempotencyStore — three-key Lua script model for HTTP idempotency
// ---------------------------------------------------------------------------

// Compile-time interface check.
var _ idemhttp.Store = (*HTTPIdempotencyStore)(nil)

// HTTPIdempotencyStore implements idemhttp.Store using a three-key Lua script
// model that stores the full HTTP response blob for replay plus the request
// fingerprint for key-reuse detection.
//
// Consistency: L1 (LocalTx) — each Lua script executes atomically within Redis.
//
//   - <store-ns>:<req-ns>:{key}:lease — SET NX with leaseTTL, value = random token. Indicates "processing".
//   - <store-ns>:<req-ns>:{key}:resp  — SET with doneTTL, value = MarshalRecordedResponse blob. Indicates "completed".
//   - <store-ns>:<req-ns>:{key}:fp    — SET with leaseTTL while processing; Record extends it to doneTTL. Value = body fingerprint; flags same-key/different-body reuse.
//
// The key prefix has two segments, both OUTSIDE the hashtag:
//
//   - <store-ns> = the construction-time KeyNamespace (s.ns), the owner
//     dimension — e.g. "_runtime" for the shared corebundle store — following
//     the same owner-prefix convention as the other adapters/redis primitives
//     (see KeyNamespace doc).
//   - <req-ns>   = the request-time ns passed to Claim (caller TenantID or the
//     "_notenant" sentinel), the tenant sub-partition.
//
// Cluster: keys are wrapped in a Redis Cluster hashtag so CRC16 hashes only
// the business-key portion; the lease, resp, and fp keys all colocate on the
// same slot, keeping multi-KEY EVAL safe under Cluster mode. Both prefix
// segments sit outside the hashtag, so slot colocality is preserved regardless
// of either namespace value.
//
// Claim compares fp only while resp or lease is active. With resp present it
// returns ClaimDone+replay after validating fp; with lease present it returns
// ClaimBusy after validating fp; when neither exists it acquires lease and
// stores fp for the leaseTTL. Record sets resp + extends fp to doneTTL + deletes
// lease. Release deletes lease + fp (token-guarded); there is no response to
// protect.
type HTTPIdempotencyStore struct {
	rdb cmdable
	ns  KeyNamespace
}

// NewHTTPIdempotencyStore creates an HTTPIdempotencyStore using the given
// Client and KeyNamespace. ns is validated up front; nil client and invalid
// namespace produce structured errors so misconfiguration fails-fast at
// composition time.
//
// The construction-time KeyNamespace (REDIS-KEY-NAMESPACE-01) must be a valid
// non-empty, lowercase, brace-free string ≤48 chars. It is the OWNER segment
// of every runtime Redis key: keys are derived as
// <store-ns>:<request-ns>:{<key>}:<role>, where <store-ns> = this namespace
// (owner dimension, e.g. "_runtime" for the shared corebundle store) and
// <request-ns> = the ns passed to Claim (caller TenantID or "_notenant"
// sentinel when using the standard Middleware).
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

// ProbeHTTPIdempotencyStoreReady is the ops-contract readiness probe name for
// the HTTP idempotency replay store. healthz.ProbeName-typed, funneled by
// PROBENAME-SEALED-FUNNEL-01. Its failure domain is distinct from redis_ready
// (bare PING on the shared client): this probe exercises the EVAL + write
// command family the Claim/Record Lua scripts depend on, so a Redis ACL that
// permits PING but denies EVAL/SET surfaces at /readyz instead of at the first
// mutating request once idempotency is default-on (gh #1469).
const ProbeHTTPIdempotencyStoreReady healthz.ProbeName = "http_idempotency_store_ready"

// readyCheckScript exercises the same EVAL + SET-with-PX command family used by
// claimRespScript / httpRecordScript. A read-only PING (the redis_ready probe)
// cannot detect an ACL that allows PING but denies EVAL or write commands — the
// exact gap this probe closes. The probe key is namespaced + hashtag-wrapped
// like every store key and auto-expires after 1 ms, leaving no residue.
const readyCheckScript = `return redis.call('SET', KEYS[1], '1', 'PX', tonumber(ARGV[1]))`

// ReadyCheck is the readiness-probe body wired via bootstrap.WithHealthChecker
// under ProbeHTTPIdempotencyStoreReady. It runs a minimal Lua EVAL that writes a
// short-lived probe key, proving both EVAL capability and write permission in
// the store's namespace. Returns nil when the store can serve Claim/Record;
// returns a structured error (→ /readyz degraded) when EVAL or SET is denied or
// Redis is unreachable.
func (s *HTTPIdempotencyStore) ReadyCheck(ctx context.Context) error {
	probeKey := s.ns.applyHashtag("readyz", "probe")
	if _, err := s.rdb.Eval(ctx, readyCheckScript, []string{probeKey}, "1").Result(); err != nil {
		return errcode.Wrap(errcode.KindInternal, ErrAdapterRedisConnect,
			"redis http idempotency store: readiness EVAL failed", err)
	}
	return nil
}

// claimRespScript is the Lua script for atomic Claim. KEYS[1] is the resp-key
// (suffix `:resp`) — the mock dispatch in http_idempotency_test.go recognizes
// the claim script by `strings.HasSuffix(keys[0], ":resp")`, consistent with
// the existing ":done" / ":lease" suffix-matching convention.
//
// KEYS[1] = <ns>:{key}:resp
// KEYS[2] = <ns>:{key}:lease
// KEYS[3] = <ns>:{key}:fp
// ARGV[1] = token
// ARGV[2] = leaseTTL (milliseconds — PX precision)
// ARGV[3] = fingerprint (hex sha256 of request body)
// ARGV[4] = fpTTL (milliseconds — leaseTTL; Record extends it to doneTTL)
//
// Returns:
//
//	{1}            = ClaimAcquired (lease set successfully; fp stored at KEYS[3] with lease TTL)
//	{0}            = ClaimBusy    (lease already held; fp matches or fp absent)
//	{2,blob}       = ClaimDone    (resp key exists; blob = stored response; fp matches or fp absent)
//	{3,fp_stored}  = FingerprintMismatch (fp key differs from ARGV[3]; fp_stored = the
//	                 stored fingerprint blob, carried back for the per-field diff)
const claimRespScript = `
local fp_stored = redis.call('GET', KEYS[3])
local resp = redis.call('GET', KEYS[1])
if resp ~= false then
  if fp_stored ~= false and ARGV[3] ~= "" and fp_stored ~= ARGV[3] then
    return {3, fp_stored}
  end
  return {2, resp}
end
local lease = redis.call('GET', KEYS[2])
if lease ~= false then
  if fp_stored ~= false and ARGV[3] ~= "" and fp_stored ~= ARGV[3] then
    return {3, fp_stored}
  end
  return {0}
end
local ok = redis.call('SET', KEYS[2], ARGV[1], 'NX', 'PX', ARGV[2])
if ok then
  if ARGV[3] ~= "" then
    redis.call('SET', KEYS[3], ARGV[3], 'PX', ARGV[4])
  end
  return {1}
end
return {0}
`

// recordScript: atomic Record (token-guarded). KEYS[1] is the lease-key.
// On success the fp key is extended to doneTTL so that future
// replay Claim calls can still validate the fingerprint. On Release the fp key
// is deleted with the lease because there is no response to protect.
//
// KEYS[1] = <ns>:{key}:lease
// KEYS[2] = <ns>:{key}:resp
// KEYS[3] = <ns>:{key}:fp
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
  if redis.call('EXISTS', KEYS[3]) == 1 then
    redis.call('PEXPIRE', KEYS[3], ARGV[3])
  end
  return 1
end
return 0
`

// httpReleaseScript: atomic Release (token-guarded). Also cleans up fp key.
//
// KEYS[1] = <ns>:{key}:lease
// KEYS[2] = <ns>:{key}:fp
// ARGV[1] = token
//
// Returns 1 on success, 0 if token mismatch.
const httpReleaseScript = `
local val = redis.call('GET', KEYS[1])
if val == ARGV[1] then
  redis.call('DEL', KEYS[1])
  redis.call('DEL', KEYS[2])
  return 1
end
return 0
`

// Claim implements idemhttp.Store. It attempts to acquire a processing lease
// for the given sealed key k.
//
// The Redis keys are derived as:
//
//	<store-ns>:<ns>:{<key>}:lease  and  …:resp  and  …:fp
//
// where <store-ns> is the construction-time KeyNamespace (s.ns, owner
// dimension), <ns> = k.Namespace() (the caller TenantID, or "_notenant" when
// absent), and <key> = k.Key() (the composed idempotency key). Both prefix
// segments sit outside the hashtag so Redis Cluster CRC16 only hashes {<key>}.
//
// Both k.Namespace() and k.Key() must be non-empty and free of '{'/'}' so the
// Redis Cluster hashtag boundary is unambiguous — a Redis-Cluster-specific
// constraint validated here, deliberately NOT folded into the store-agnostic
// DeriveKey (MemStore has no such constraint).
//
// fingerprint is hex(sha256(body)). If a previous Claim stored a different
// fingerprint for the same key, ErrFingerprintMismatch is returned.
func (s *HTTPIdempotencyStore) Claim(
	ctx context.Context, k idemhttp.IdempotencyKey, fingerprint string, leaseTTL time.Duration,
) (idempotency.ClaimState, *idemhttp.RecordedResponse, idemhttp.Receipt, error) {
	ns, key := k.Namespace(), k.Key()
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

	// Derive Redis keys: <store-ns>:<req-ns>:{<key>}:<role>
	// scopedNS folds the construction-time owner namespace (s.ns) and the
	// request-time tenant ns into a single "<owner>:<tenant>" prefix; both sit
	// outside the hashtag so the business key alone drives Redis Cluster slot
	// colocation (lease + resp + fp on same slot).
	scopedNS := s.ns.apply(ns)
	respKey := KeyNamespace(scopedNS).applyHashtag(key, "resp")
	leaseKey := KeyNamespace(scopedNS).applyHashtag(key, "lease")
	fpKey := KeyNamespace(scopedNS).applyHashtag(key, "fp")
	leaseMs := max(leaseTTL.Milliseconds(), 1)
	// fpTTL follows the in-flight lease. If the handler never records a response
	// and the lease expires, the old fingerprint must expire with it so a later
	// retry is a fresh acquisition. Record extends the fp key to doneTTL.
	fpMs := leaseMs

	res, err := s.rdb.Eval(ctx, claimRespScript, []string{respKey, leaseKey, fpKey}, token, leaseMs, fingerprint, fpMs).Result()
	if err != nil {
		return 0, nil, nil, classifyRedisError(err, ErrAdapterRedisSet, "http idempotency claim")
	}

	return s.decodeClaim(res, respKey, leaseKey, fpKey, token)
}

// Lua claim-result codes. claimRespScript always returns a Lua table, which
// go-redis surfaces as a []any: {1}→[]any{int64(1)} (acquired),
// {0}→[]any{int64(0)} (busy), {2,blob}→[]any{int64(2), string} (done),
// {3,fp}→[]any{int64(3), string} (fingerprint mismatch; fp = stored blob). A
// single-element Lua table is NOT flattened to a bare int64 by real Redis — the
// reply is always a multi-bulk array — so decodeClaim treats every reply as a
// slice and switches on the leading code element.
const (
	claimCodeBusy                int64 = 0
	claimCodeAcquired            int64 = 1
	claimCodeDone                int64 = 2
	claimCodeFingerprintMismatch int64 = 3
)

// decodeClaim decodes the Lua result from claimRespScript. The reply is always
// a slice (see the claimCode* doc); a bare int64 is never produced by real
// Redis for this array reply, and the unit-test mock mirrors the []any shape so
// the decode path is identical under mock and live Redis.
func (s *HTTPIdempotencyStore) decodeClaim(
	res any, respKey, leaseKey, fpKey, token string,
) (idempotency.ClaimState, *idemhttp.RecordedResponse, idemhttp.Receipt, error) {
	arr, ok := res.([]any)
	if !ok || len(arr) == 0 {
		return 0, nil, nil, errcode.New(errcode.KindInternal, ErrAdapterRedisGet,
			"redis: http idempotency claim unexpected result type")
	}
	code, ok := arr[0].(int64)
	if !ok {
		return 0, nil, nil, errcode.New(errcode.KindInternal, ErrAdapterRedisGet,
			"redis: http idempotency claim reply code not an integer")
	}
	switch code {
	case claimCodeAcquired:
		r := &httpReceipt{rdb: s.rdb, leaseKey: leaseKey, respKey: respKey, fpKey: fpKey, token: token}
		return idempotency.ClaimAcquired, nil, r, nil //nolint:nilnil // ClaimState discriminates; rec nil unless ClaimDone
	case claimCodeBusy:
		return idempotency.ClaimBusy, nil, noopHTTPReceipt{}, nil //nolint:nilnil // ClaimState discriminates; rec nil unless ClaimDone
	case claimCodeDone:
		return decodeClaimDone(arr)
	case claimCodeFingerprintMismatch:
		return decodeClaimFingerprintMismatch(arr)
	default:
		return 0, nil, nil, errcode.New(errcode.KindInternal, ErrAdapterRedisGet,
			"redis: http idempotency claim unexpected result code")
	}
}

// decodeClaimDone decodes the {2, blob} ClaimDone reply (code already matched).
func decodeClaimDone(arr []any) (idempotency.ClaimState, *idemhttp.RecordedResponse, idemhttp.Receipt, error) {
	if len(arr) < 2 {
		return 0, nil, nil, errcode.New(errcode.KindInternal, ErrAdapterRedisGet,
			"redis: http idempotency claim done reply missing response blob")
	}
	blob, ok := arr[1].(string)
	if !ok {
		return 0, nil, nil, errcode.New(errcode.KindInternal, ErrAdapterRedisGet,
			"redis: http idempotency claim done reply blob not a string")
	}
	rec, err := idemhttp.UnmarshalRecordedResponse([]byte(blob))
	if err != nil {
		return 0, nil, nil, errcode.Wrap(errcode.KindInternal, ErrAdapterRedisGet,
			"redis: http idempotency claim replay decode failed", err)
	}
	return idempotency.ClaimDone, &rec, noopHTTPReceipt{}, nil
}

// decodeClaimFingerprintMismatch decodes the {3, fp_stored} reply (code already
// matched) into a *idemhttp.FingerprintMismatchError carrying the stored
// fingerprint blob, so the middleware can compute a per-field diff. A missing or
// non-string fp element degrades to an empty Stored (the base 422 is still
// returned, just without the per-field diff).
func decodeClaimFingerprintMismatch(arr []any) (idempotency.ClaimState, *idemhttp.RecordedResponse, idemhttp.Receipt, error) {
	stored := ""
	if len(arr) >= 2 {
		if s, ok := arr[1].(string); ok {
			stored = s
		}
	}
	return 0, nil, nil, &idemhttp.FingerprintMismatchError{Stored: stored}
}

// ---------------------------------------------------------------------------
// httpReceipt — Receipt for ClaimAcquired
// ---------------------------------------------------------------------------

// httpReceipt implements idemhttp.Receipt for an acquired lease.
type httpReceipt struct {
	rdb      cmdable
	leaseKey string
	respKey  string
	fpKey    string // fingerprint key; may be empty when fingerprint feature not used
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
// The fp key TTL is extended to doneTTL so fingerprint validation still works
// for replay Claim calls after the lease is gone.
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
	fpKey := r.fpKey
	if fpKey == "" {
		fpKey = r.leaseKey // fallback: Lua EXISTS check will be false, PEXPIRE skipped
	}
	res, err := r.rdb.Eval(ctx, httpRecordScript, []string{r.leaseKey, r.respKey, fpKey}, r.token, string(blob), doneTTLMs).Result()
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

// Release removes the processing lease (and fp key) without recording a response.
// Repeat calls after a successful Release are no-ops (return nil).
// Stale-lease errors are cached (permanent). Redis timeouts are NOT cached,
// allowing retry with a fresh ctx.
func (r *httpReceipt) Release(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.released {
		return r.releaseErr
	}
	fpKey := r.fpKey
	if fpKey == "" {
		fpKey = r.leaseKey // fallback key for script arity; DEL on missing key is a no-op
	}
	res, err := r.rdb.Eval(ctx, httpReleaseScript, []string{r.leaseKey, fpKey}, r.token).Result()
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
