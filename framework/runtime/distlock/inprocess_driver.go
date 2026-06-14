package distlock

import (
	"context"
	"sync"
	"time"

	"github.com/ghbvf/gocell/framework/kernel/clock"
)

// InProcessDriver is a single-process, in-memory [Driver]: token-ownership and
// TTL semantics held in a map guarded by a mutex, with no network I/O.
//
// It is the single-pod backend for a [Locker] in demo / single-pod topology —
// notably the saga-projection Tailer's per-projection leader gate, which requires
// a non-nil Locker even when there is only one process (there is no nil-Locker
// "always leader" bypass, unlike the saga Coordinator). In a real multi-pod
// deployment this driver is WRONG (each pod would believe it holds every lock);
// multi-pod topology must use the Redis-backed driver. To keep that boundary
// machine-enforced rather than a footgun, construction of this driver is funneled
// through cellmodules/sagaprojectiondeps.Resolve's demo branch and guarded by the
// SAGA-PROJECTION-DEPS-INMEM-FUNNEL-01 archtest (the same sealed-single-pod-
// primitive discipline as idempotency.NewInMemClaimer via replaydeps).
//
// It passes locktest.RunDriverConformance (C-1..C-4, C-7) identically to the
// Redis driver. TTL expiry is driven by the injected clock.Clock, so the
// manager's renew/expiry cycle is exercised the same way as locktest.FakeDriver
// (TTL physics conformance is not applicable to a clock-injected driver).
type InProcessDriver struct {
	clk clock.Clock
	mu  sync.Mutex
	// keys maps lock key -> current holder. A key is held iff present AND not
	// past its expiresAt (lazily expired on access).
	keys map[string]inProcessEntry
}

type inProcessEntry struct {
	token     string
	expiresAt time.Time
}

// compile-time assertion: InProcessDriver satisfies Driver.
var _ Driver = (*InProcessDriver)(nil)

// NewInProcessDriver returns an in-process Driver using clk for TTL expiry.
// clk is a positional dependency (per go-standards: no WithClock option / Config
// field) and is required.
func NewInProcessDriver(clk clock.Clock) *InProcessDriver {
	clock.MustHaveClock(clk, "distlock.NewInProcessDriver")
	return &InProcessDriver{clk: clk, keys: make(map[string]inProcessEntry)}
}

// SetNX acquires key for token with the given TTL, succeeding only if the key is
// unheld or its lease has expired. Implements Driver.SetNX.
func (d *InProcessDriver) SetNX(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.clk.Now()
	if e, ok := d.keys[key]; ok && now.Before(e.expiresAt) {
		// Still held by a live lease (possibly token itself — SetNX is not a
		// renew, so a re-acquire of one's own live key is reported busy, matching
		// the Redis SET NX semantics).
		return false, nil
	}
	d.keys[key] = inProcessEntry{token: token, expiresAt: now.Add(ttl)}
	return true, nil
}

// Renew extends key's TTL only if token still matches a live lease. Implements
// Driver.Renew.
func (d *InProcessDriver) Renew(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	now := d.clk.Now()
	e, ok := d.keys[key]
	if !ok || !now.Before(e.expiresAt) || e.token != token {
		// Gone, expired, or held by a different token.
		return false, nil
	}
	e.expiresAt = now.Add(ttl)
	d.keys[key] = e
	return true, nil
}

// Release deletes key only if token matches; idempotent when the key is gone or
// held by another token. Implements Driver.Release.
func (d *InProcessDriver) Release(ctx context.Context, key, token string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()

	if e, ok := d.keys[key]; ok && e.token == token {
		delete(d.keys, key)
	}
	return nil
}
