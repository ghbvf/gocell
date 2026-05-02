// Package ratelimit implements a per-key in-process token bucket rate limiter.
//
// The bucket math (refill, burst cap, AllowN) reads time exclusively through
// the injected clock.Clock — both the cleanup ticker and the per-key
// bucket.lastRefill state advance only when the injected clock advances.
// FakeClock-driven tests can therefore exhaustively exercise burst/recovery
// behavior without sleeping.
//
// ref: ADR docs/architecture/202605021500-adr-kernel-clock-injection.md
// (D6 PROD-CLOCK-INJECTION-01) — adapters/ratelimit replaced
// golang.org/x/time/rate.Limiter (which hard-codes time.Now() in its internal
// reserveN path) with this self-contained bucket so the D6 contract holds
// end-to-end at the rate-limit boundary.
package ratelimit

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/runtime/http/middleware"
)

const (
	// defaultRateLimitStaleAfter is the duration after which an idle per-key
	// entry is considered stale and eligible for cleanup.
	defaultRateLimitStaleAfter = 5 * time.Minute
)

// Compile-time checks.
var (
	_ middleware.RateLimiter         = (*Limiter)(nil)
	_ middleware.WindowedRateLimiter = (*Limiter)(nil)
	_ lifecycle.ContextCloser        = (*Limiter)(nil)
)

// Config holds settings for the token bucket rate limiter.
type Config struct {
	// Rate is the number of requests allowed per second per key. Default: 10.
	Rate float64

	// Burst is the maximum number of tokens that can be consumed in a single
	// burst. Default: 20.
	Burst int

	// CleanupInterval is how often to scan for stale entries. Default: 1m.
	CleanupInterval time.Duration

	// StaleAfter is the idle duration after which a per-key limiter is removed.
	// Default: 5m.
	StaleAfter time.Duration
}

func (c *Config) defaults() {
	if c.Rate <= 0 {
		c.Rate = 10
	}
	if c.Burst <= 0 {
		c.Burst = 20
	}
	if c.CleanupInterval <= 0 {
		c.CleanupInterval = time.Minute
	}
	if c.StaleAfter <= 0 {
		c.StaleAfter = defaultRateLimitStaleAfter
	}
}

// bucket is a self-contained token bucket. It is not safe for concurrent use
// on its own; callers must hold the parent Limiter's mu lock.
type bucket struct {
	tokens     float64
	lastRefill time.Time
}

// allowN refills tokens based on elapsed time and returns whether n tokens are
// available. Semantics match golang.org/x/time/rate.Limiter.Allow() with the
// token-refill-on-call approach: each call may add tokens proportional to
// elapsed wall-clock time before checking availability.
func (b *bucket) allowN(now time.Time, ratePerSec float64, burst float64, n int) bool {
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens = math.Min(burst, b.tokens+elapsed*ratePerSec)
	b.lastRefill = now
	if b.tokens < float64(n) {
		return false
	}
	b.tokens -= float64(n)
	return true
}

type entry struct {
	bucket   bucket
	lastSeen time.Time
}

// Limiter implements middleware.RateLimiter and middleware.WindowedRateLimiter
// using per-key token buckets with a self-contained token bucket algorithm.
type Limiter struct {
	rate  float64
	burst int

	mu       sync.RWMutex
	limiters map[string]*entry

	staleAfter time.Duration
	stopOnce   sync.Once
	stopCh     chan struct{}
	clock      clock.Clock
}

// New creates a per-IP token bucket rate limiter. It starts a background
// goroutine for stale entry cleanup. Call Close() to stop cleanup.
func New(cfg Config, clk clock.Clock) *Limiter {
	clock.MustHaveClock(clk, "ratelimit.New")
	cfg.defaults()
	l := &Limiter{
		rate:       cfg.Rate,
		burst:      cfg.Burst,
		limiters:   make(map[string]*entry),
		staleAfter: cfg.StaleAfter,
		stopCh:     make(chan struct{}),
		clock:      clk,
	}
	go l.cleanup(cfg.CleanupInterval)
	return l
}

// Allow checks whether the request identified by key should be allowed.
func (l *Limiter) Allow(key string) bool {
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.limiters[key]
	if !ok {
		e = &entry{
			bucket: bucket{
				tokens:     float64(l.burst),
				lastRefill: now,
			},
			lastSeen: now,
		}
		l.limiters[key] = e
	}
	e.lastSeen = now
	return e.bucket.allowN(now, l.rate, float64(l.burst), 1)
}

// Window returns the rate limit window and limit, satisfying
// middleware.WindowedRateLimiter. The window is always 1 second; the limit
// is the configured rate (tokens per second).
func (l *Limiter) Window() (time.Duration, int) {
	return time.Second, int(l.rate)
}

// Close stops the background cleanup goroutine.
//
// Close is non-blocking: close(l.stopCh) is O(1) and the refill goroutine
// will observe the signal at its next tick and exit. The ctx parameter is
// accepted for lifecycle.ContextCloser compatibility but not consumed
// because stopCh close is synchronous and cannot be short-circuited safely.
// Distinct from InMemoryEventBus.Close (which ignores ctx for similar reasons)
// and Subscriber.Close (which honors ctx to bound in-flight drain).
//
// Close is idempotent: concurrent and repeated calls are safe.
//
// ref: uber-go/fx app.go StopTimeout — ctx as shared shutdown budget.
// ref: uber-go/fx lifecycle OnStop(ctx) — ContextCloser pattern.
func (l *Limiter) Close(_ context.Context) error {
	l.stopOnce.Do(func() { close(l.stopCh) })
	return nil
}

// Len returns the number of tracked keys. Exported for testing.
func (l *Limiter) Len() int {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return len(l.limiters)
}

func (l *Limiter) cleanup(interval time.Duration) {
	ticker := l.clock.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C():
			l.removeStale()
		}
	}
}

func (l *Limiter) removeStale() {
	now := l.clock.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	for key, e := range l.limiters {
		if now.Sub(e.lastSeen) > l.staleAfter {
			delete(l.limiters, key)
		}
	}
}
