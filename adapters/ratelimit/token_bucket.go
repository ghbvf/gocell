package ratelimit

import (
	"context"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/runtime/http/middleware"
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
		c.StaleAfter = 5 * time.Minute
	}
}

type entry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// Limiter implements middleware.RateLimiter and middleware.WindowedRateLimiter
// using per-key token buckets backed by golang.org/x/time/rate.
type Limiter struct {
	rate  float64
	burst int

	mu       sync.RWMutex
	limiters map[string]*entry

	staleAfter time.Duration
	stopOnce   sync.Once
	stopCh     chan struct{}
}

// New creates a per-IP token bucket rate limiter. It starts a background
// goroutine for stale entry cleanup. Call Close() to stop cleanup.
func New(cfg Config) *Limiter {
	cfg.defaults()
	l := &Limiter{
		rate:       cfg.Rate,
		burst:      cfg.Burst,
		limiters:   make(map[string]*entry),
		staleAfter: cfg.StaleAfter,
		stopCh:     make(chan struct{}),
	}
	go l.cleanup(cfg.CleanupInterval)
	return l
}

// Allow checks whether the request identified by key should be allowed.
func (l *Limiter) Allow(key string) bool {
	e := l.getOrCreate(key)
	return e.limiter.Allow()
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

func (l *Limiter) getOrCreate(key string) *entry {
	now := time.Now()

	l.mu.RLock()
	e, ok := l.limiters[key]
	l.mu.RUnlock()
	if ok {
		l.mu.Lock()
		e.lastSeen = now
		l.mu.Unlock()
		return e
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	// Double-check after acquiring write lock.
	if e, ok = l.limiters[key]; ok {
		e.lastSeen = now
		return e
	}

	e = &entry{
		limiter:  rate.NewLimiter(rate.Limit(l.rate), l.burst),
		lastSeen: now,
	}
	l.limiters[key] = e
	return e
}

func (l *Limiter) cleanup(interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-l.stopCh:
			return
		case <-ticker.C:
			l.removeStale()
		}
	}
}

func (l *Limiter) removeStale() {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()

	for key, e := range l.limiters {
		if now.Sub(e.lastSeen) > l.staleAfter {
			delete(l.limiters, key)
		}
	}
}
