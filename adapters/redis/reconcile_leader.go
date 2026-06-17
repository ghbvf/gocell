package redis

import (
	"context"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/reconcile"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/idutil"
)

// Compile-time assertion: *RedisReconcileElector satisfies reconcile.LeaderElector.
var _ reconcile.LeaderElector = (*RedisReconcileElector)(nil)

// reconcileEpochKeyTTL is the TTL set on the epoch key (KEYS[2]) on every
// acquire. The epoch key must outlive any lease so fencing is never broken by
// Redis eviction, but it must have SOME TTL so it is not permanently retained
// under allkeys-* eviction policies. 30 days is far longer than any realistic
// lease TTL (typically seconds to minutes) while still being evictable when
// truly stale.
const reconcileEpochKeyTTL = 30 * 24 * time.Hour

func reconcileEpochKeyTTLSeconds() (int64, error) {
	if reconcileEpochKeyTTL < time.Second {
		return 0, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"redis reconcile elector: epoch key TTL must be at least 1s")
	}
	return int64(reconcileEpochKeyTTL / time.Second), nil
}

// reconcileAcquireScript atomically acquires the leader lease and returns
// {acquired, epoch}. The monotonic epoch (KEYS[2]) is INCR'd only when the holder
// key (KEYS[1]) is free — i.e. on a real holder change (free / TTL-expired /
// released). A same-holder re-acquire of a still-live lease refreshes the TTL and
// keeps the epoch (idempotent). Contention (held by another holder) returns
// {0, 0}. This is the redis half of the monotonic-fencing model in
// reconcile.LeaseToken.Epoch.
//
//	KEYS[1] = holder key   KEYS[2] = epoch key
//	ARGV[1] = holderID     ARGV[2] = lease TTL (ms)   ARGV[3] = epoch key TTL (s)
//
// The epoch key is given a long TTL (ARGV[3]) on every acquire AND every renew
// (see reconcileRenewScript) so it survives Redis allkeys-* eviction AND never
// expires under a long-held leader. Without a refreshed TTL the key can be evicted
// or simply lapse mid-term, resetting the monotonic counter to 0 and breaking
// fencing (PR-A6 review C1/F1).
//
// Same-holder fail-closed: a same-holder re-acquire MUST find the epoch key
// present — ReleaseLease never deletes it and RenewLease keeps refreshing its TTL,
// so the only way it is absent while this holder still owns the lease is a true
// anomaly (Redis eviction or operational deletion). The branch therefore returns
// a Lua error_reply rather than coercing a missing epoch to 0: silently rolling
// the holder's monotonic Epoch backward to 0 is fail-OPEN (a zombie write fenced
// at the lower epoch is mis-accepted/rejected and the holder believes it is still
// a valid leader). The error surfaces as a non-ErrLeaseHeld acquire failure, which
// leaderManage treats as fail-closed (no dispatch) + logs at Warn (operator-visible)
// before backing off; the stale holder key is left untouched so it lapses and the
// next acquire takes the free-holder path cleanly. (PR-A6 round-3 review.)
//
// Residual, by design — NOT closed here: the free-holder branch INCRs a missing
// epoch key from nil to 1, so a takeover AFTER an eviction resets the monotonic
// counter regardless. That branch CANNOT be made fail-closed in Redis: a genuine
// first-ever acquire is indistinguishable from a post-eviction takeover (both see
// no epoch key). Redis-backed monotonic-epoch fencing is therefore best-effort
// under eviction; the durable monotonic guarantee lives in the Postgres adapter
// (reconcile_leases row, persistent, never deleted on release). See ADR
// 202605291600-661 §threat-matrix (T-DUAL/T-FENCE) Redis-eviction residual.
//
// Both keys share a Redis Cluster hash tag ({reconcilerID}) so they colocate on a
// single slot — a multi-key Lua script touching two different slots is a CROSSSLOT
// error on Redis Cluster (PR-A6 review C1/F2).
const reconcileAcquireScript = `
local cur = redis.call("GET", KEYS[1])
if cur == false then
    redis.call("SET", KEYS[1], ARGV[1], "PX", ARGV[2])
    local e = redis.call("INCR", KEYS[2])
    redis.call("EXPIRE", KEYS[2], ARGV[3])
    return {1, e}
elseif cur == ARGV[1] then
    local e = redis.call("GET", KEYS[2])
    if e == false then return redis.error_reply("reconcile epoch key missing on same-holder re-acquire") end
    redis.call("PEXPIRE", KEYS[1], ARGV[2])
    redis.call("EXPIRE", KEYS[2], ARGV[3])
    return {1, tonumber(e)}
else
    return {0, 0}
end
`

// reconcileRenewScript extends the holder-key TTL AND refreshes the epoch-key TTL
// (so the monotonic epoch never lapses while a leader keeps renewing — PR-A6
// review C1/F1), ownership-guarded. Returns 1 if still held, 0 if ownership lost.
// Two keys, colocated via the shared {reconcilerID} hash tag.
//
// Renew is the DOMINANT path for a long-held leader (it renews far more often than
// it re-acquires), so the epoch-key fail-closed guard must live here too — not only
// in reconcileAcquireScript's same-holder branch. A still-owned holder whose epoch
// key has vanished (eviction / operational deletion) is the same anomaly: the bare
// EXPIRE KEYS[2] would no-op on a missing key (Redis returns 0) yet the script would
// still return 1, so the leader keeps renewing while its monotonic token has silently
// disappeared from Redis — and on the next handoff the free-holder branch rebuilds
// the epoch from 1, inverting fencing against this leader's still-live token (epoch
// N). The branch therefore returns a Lua error_reply when the epoch key is absent;
// RenewLease surfaces it as a non-ErrReconcileLeaseLost renew error, renewLoop ends
// the term (lease-ctx cancel) and leaderManage re-acquires — the stale holder key is
// left to lapse so the next acquire takes the free-holder path cleanly.
//
//	KEYS[1] = holder key   KEYS[2] = epoch key
//	ARGV[1] = holderID     ARGV[2] = lease TTL (ms)   ARGV[3] = epoch key TTL (s)
const reconcileRenewScript = `
if redis.call("GET", KEYS[1]) == ARGV[1] then
    if redis.call("GET", KEYS[2]) == false then return redis.error_reply("reconcile epoch key missing on renew") end
    redis.call("PEXPIRE", KEYS[1], ARGV[2])
    redis.call("EXPIRE", KEYS[2], ARGV[3])
    return 1
else
    return 0
end
`

// RedisReconcileElector implements reconcile.LeaderElector using Redis SET NX PX
// for the holder lease and an INCR'd epoch key for the monotonic fencing token.
// Release reuses the package's ownership-guarded releaseLockScript (the epoch key
// is never deleted on release so a subsequent holder always observes a strictly
// higher epoch); acquire/renew use the reconcile-specific scripts above.
//
// Both keys are KeyNamespace-prefixed AND share a {reconcilerID} hash tag so they
// colocate on one Redis Cluster slot: the holder key is "<ns>:{<rid>}:lease" and
// the epoch key is "<ns>:{<rid>}:epoch".
type RedisReconcileElector struct {
	rdb      cmdable
	ns       KeyNamespace
	holderID string
	lease    reconcile.LeaseTTL
	clk      clock.Clock
}

// NewRedisReconcileElector builds a leader elector. The holderID (this replica's
// identity) is minted INTERNALLY as a fresh UUID, so accidental cross-process
// holderID reuse (treated as the same holder, defeating mutual exclusion — PR-A6
// review C4) is impossible by construction. lease is the validated lease TTL (a
// sealed reconcile.LeaseTTL, so a sub-millisecond window — which would truncate to a
// 0ms PX TTL and break mutual exclusion — is unrepresentable); the reconcile Loop
// derives its renew cadence as TTL/3 unless overridden. clk is the injected clock
// stamping the token window (Redis has no server-side wall clock to return).
// ns / client / lease / clk are validated up front.
func NewRedisReconcileElector(
	client *Client, ns KeyNamespace, lease reconcile.LeaseTTL, clk clock.Clock,
) (*RedisReconcileElector, error) {
	if client == nil {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterRedisConnect, "redis reconcile elector: client is nil")
	}
	return newReconcileElectorFromCmdable(client.cmdable(), ns, lease, clk)
}

// newReconcileElectorFromCmdable is the cmdable-level constructor used by unit
// tests that inject a mock cmdable. Same validation contract as the public
// constructor (mirrors newRedisDriverFromCmdable). holderID is minted internally.
func newReconcileElectorFromCmdable(
	rdb cmdable, ns KeyNamespace, lease reconcile.LeaseTTL, clk clock.Clock,
) (*RedisReconcileElector, error) {
	if err := ns.Validate(); err != nil {
		return nil, err
	}
	if rdb == nil {
		return nil, errcode.New(errcode.KindInternal, ErrAdapterRedisConnect, "redis reconcile elector: cmdable is nil")
	}
	if lease.IsZero() {
		// ErrCellInvalidConfig (not the connect code): a zero-value lease is a
		// wiring mistake (forgot reconcile.NewLeaseTTL), routable separately from
		// genuine Redis connection failures.
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"redis reconcile elector: lease must be a constructed LeaseTTL")
	}
	clock.MustHaveClock(clk, "redis reconcile elector")
	holderID, err := idutil.NewUUID()
	if err != nil {
		return nil, errcode.Wrap(errcode.KindInternal, ErrAdapterRedisConnect, "redis reconcile elector: mint holderID", err)
	}
	slog.Info("redis reconcile elector: created", slog.String("holder_id", holderID))
	return &RedisReconcileElector{rdb: rdb, ns: ns, holderID: holderID, lease: lease, clk: clk}, nil
}

func (e *RedisReconcileElector) holderKey(reconcilerID string) string {
	return e.ns.applyHashtag(reconcilerID, "lease")
}

func (e *RedisReconcileElector) epochKey(reconcilerID string) string {
	return e.ns.applyHashtag(reconcilerID, "epoch")
}

// AcquireLease implements reconcile.LeaderElector.
func (e *RedisReconcileElector) AcquireLease(ctx context.Context, reconcilerID string) (reconcile.LeaseToken, error) {
	now := e.clk.Now()
	epochTTLSeconds, err := reconcileEpochKeyTTLSeconds()
	if err != nil {
		return reconcile.LeaseToken{}, err
	}
	res, err := e.rdb.Eval(ctx, reconcileAcquireScript,
		[]string{e.holderKey(reconcilerID), e.epochKey(reconcilerID)},
		e.holderID, e.lease.Milliseconds(), epochTTLSeconds).Slice()
	if err != nil {
		return reconcile.LeaseToken{}, classifyRedisError(err, ErrAdapterRedisSet, "redis reconcile elector acquire")
	}
	acquired, epoch, err := parseAcquireResult(res)
	if err != nil {
		return reconcile.LeaseToken{}, err
	}
	if !acquired {
		return reconcile.LeaseToken{}, reconcile.ErrLeaseHeld
	}
	return reconcile.LeaseToken{
		ReconcilerID: reconcilerID,
		HolderID:     e.holderID,
		Epoch:        epoch,
		AcquiredAt:   now,
		ExpiresAt:    now.Add(e.lease.Duration()),
	}, nil
}

// RenewLease implements reconcile.LeaderElector (ownership-guarded TTL extend for
// BOTH the holder key and the epoch key, epoch value unchanged). Refreshing the
// epoch-key TTL here is what keeps the monotonic counter alive under a long-held
// leader. Returns ErrReconcileLeaseLost when no longer the holder.
func (e *RedisReconcileElector) RenewLease(ctx context.Context, token reconcile.LeaseToken) error {
	epochTTLSeconds, err := reconcileEpochKeyTTLSeconds()
	if err != nil {
		return err
	}
	held, err := e.rdb.Eval(ctx, reconcileRenewScript,
		[]string{e.holderKey(token.ReconcilerID), e.epochKey(token.ReconcilerID)},
		e.holderID, e.lease.Milliseconds(), epochTTLSeconds).Int64()
	if err != nil {
		return classifyRedisError(err, ErrAdapterRedisSet, "redis reconcile elector renew")
	}
	if held != 1 {
		return reconcile.ErrReconcileLeaseLost
	}
	return nil
}

// ReleaseLease implements reconcile.LeaderElector (ownership-guarded DEL of the
// holder key; the epoch key persists so the next holder sees a higher epoch).
// Idempotent: releasing a lease already lost is not an error.
func (e *RedisReconcileElector) ReleaseLease(ctx context.Context, token reconcile.LeaseToken) error {
	if _, err := e.rdb.Eval(ctx, releaseLockScript,
		[]string{e.holderKey(token.ReconcilerID)}, e.holderID).Int64(); err != nil {
		return classifyRedisError(err, ErrAdapterRedisDelete, "redis reconcile elector release")
	}
	return nil
}

// parseAcquireResult decodes the {acquired, epoch} table the acquire Lua returns.
// go-redis decodes Lua integers as int64.
func parseAcquireResult(res []any) (acquired bool, epoch uint64, err error) {
	if len(res) != 2 {
		return false, 0, errcode.New(errcode.KindInternal, ErrAdapterRedisConnect,
			"redis reconcile elector: malformed acquire result")
	}
	a, ok := res[0].(int64)
	if !ok {
		return false, 0, errcode.New(errcode.KindInternal, ErrAdapterRedisConnect,
			"redis reconcile elector: malformed acquire flag")
	}
	if a != 1 {
		return false, 0, nil
	}
	ep, ok := res[1].(int64)
	if !ok || ep < 0 {
		return false, 0, errcode.New(errcode.KindInternal, ErrAdapterRedisConnect,
			"redis reconcile elector: malformed acquire epoch")
	}
	return true, uint64(ep), nil
}
