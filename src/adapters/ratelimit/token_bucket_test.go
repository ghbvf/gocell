package ratelimit

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/runtime/http/middleware"
)

// Compile-time checks.
var (
	_ middleware.RateLimiter         = (*Limiter)(nil)
	_ middleware.WindowedRateLimiter = (*Limiter)(nil)
)

func TestLimiter_AllowsWithinRate(t *testing.T) {
	l := New(Config{Rate: 10, Burst: 10})
	defer l.Close()

	for i := range 10 {
		assert.True(t, l.Allow("10.0.0.1"), "request %d within burst should be allowed", i)
	}
}

func TestLimiter_RejectsOverRate(t *testing.T) {
	l := New(Config{Rate: 1, Burst: 1})
	defer l.Close()

	assert.True(t, l.Allow("10.0.0.1"), "first request should be allowed")
	assert.False(t, l.Allow("10.0.0.1"), "second request should be rejected (burst exhausted)")
}

func TestLimiter_PerIPIsolation(t *testing.T) {
	l := New(Config{Rate: 1, Burst: 1})
	defer l.Close()

	assert.True(t, l.Allow("10.0.0.1"), "first IP first request")
	assert.True(t, l.Allow("10.0.0.2"), "second IP first request — independent bucket")
	assert.False(t, l.Allow("10.0.0.1"), "first IP second request — burst exhausted")
}

func TestLimiter_Window(t *testing.T) {
	l := New(Config{Rate: 100, Burst: 200})
	defer l.Close()

	window, limit := l.Window()
	assert.Equal(t, time.Second, window, "window should be 1 second")
	assert.Equal(t, 100, limit, "limit should match rate")
}

func TestLimiter_StaleEntryCleanup(t *testing.T) {
	l := New(Config{
		Rate:            10,
		Burst:           10,
		CleanupInterval: 50 * time.Millisecond,
		StaleAfter:      100 * time.Millisecond,
	})
	defer l.Close()

	// Create entries.
	l.Allow("stale-ip-1")
	l.Allow("stale-ip-2")
	assert.Equal(t, 2, l.Len(), "should have 2 entries")

	// Wait for stale + cleanup cycle.
	time.Sleep(200 * time.Millisecond)

	assert.Equal(t, 0, l.Len(), "stale entries should be cleaned up")
}

func TestLimiter_ConcurrentAccess(t *testing.T) {
	l := New(Config{Rate: 1000, Burst: 1000})
	defer l.Close()

	var wg sync.WaitGroup
	for i := range 100 {
		wg.Add(1)
		go func(ip string) {
			defer wg.Done()
			for range 10 {
				l.Allow(ip)
			}
		}("10.0.0." + itoa(i))
	}
	wg.Wait()
}

func TestLimiter_DefaultConfig(t *testing.T) {
	l := New(Config{}) // zero-value config → sensible defaults
	defer l.Close()

	// Should not panic and should allow requests.
	require.True(t, l.Allow("default-test"))

	window, limit := l.Window()
	assert.Equal(t, time.Second, window)
	assert.Greater(t, limit, 0, "default rate must be positive")
}

// itoa is a minimal int-to-string for test IP generation.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [3]byte
	i := len(b) - 1
	for n > 0 {
		b[i] = byte('0' + n%10)
		n /= 10
		i--
	}
	return string(b[i+1:])
}
