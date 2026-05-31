package reconcile

import (
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBackoff_ExponentialBounded verifies the client-go mirrored sequence:
// first call returns base (5ms), then 10ms, 20ms, … capped at max (1000s).
// Sequence must be monotone non-decreasing and never negative or zero.
func TestBackoff_ExponentialBounded(t *testing.T) {
	const (
		base = 5 * time.Millisecond
		max  = 1000 * time.Second
	)
	b := newEntityBackoff(base, max)
	entity := "entity-a"

	prev := time.Duration(0)
	for i := 0; i < 30; i++ {
		d := b.When(entity)
		require.Greater(t, d, time.Duration(0), "backoff must never be zero or negative (call %d)", i)
		assert.GreaterOrEqual(t, d, prev, "backoff must be monotone non-decreasing (call %d)", i)
		assert.LessOrEqual(t, d, max, "backoff must be capped at max (call %d)", i)
		prev = d
	}
	// After enough calls, result must be exactly max.
	last := b.When(entity)
	assert.Equal(t, max, last, "backoff must reach and stay at max")

	// Verify the exact initial sequence: 5ms, 10ms, 20ms, 40ms, 80ms, 160ms.
	b2 := newEntityBackoff(base, max)
	entity2 := "seq-check"
	expected := []time.Duration{
		5 * time.Millisecond,
		10 * time.Millisecond,
		20 * time.Millisecond,
		40 * time.Millisecond,
		80 * time.Millisecond,
		160 * time.Millisecond,
	}
	for i, want := range expected {
		got := b2.When(entity2)
		assert.Equal(t, want, got, "backoff sequence call %d", i)
	}
}

// TestBackoff_ResetOnSuccess verifies that Forget resets the counter so the
// next When call returns base again.
func TestBackoff_ResetOnSuccess(t *testing.T) {
	b := newEntityBackoff(5*time.Millisecond, 1000*time.Second)
	entity := "reset-entity"

	// Grow the backoff a few times.
	b.When(entity)       // 5ms (n=0 before call → counter becomes 1)
	b.When(entity)       // 10ms
	b.When(entity)       // 20ms
	d1 := b.When(entity) // 40ms
	assert.Greater(t, d1, 5*time.Millisecond, "backoff should have grown")

	// After Forget, next When should return base.
	b.Forget(entity)
	d2 := b.When(entity)
	assert.Equal(t, 5*time.Millisecond, d2, "Forget must reset backoff to base")
}

// TestBackoff_Defaults verifies that zero or negative base/max get clamped to
// the defaults (5ms and 1000s).
func TestBackoff_Defaults(t *testing.T) {
	b := newEntityBackoff(0, 0) // should use defaults
	d := b.When("e")
	assert.Equal(t, 5*time.Millisecond, d, "zero base should use default 5ms")
}

// TestBackoff_ConcurrencySmoke verifies that concurrent When/Forget calls do
// not race (run with -race).
func TestBackoff_ConcurrencySmoke(t *testing.T) {
	b := newEntityBackoff(5*time.Millisecond, 1000*time.Second)
	entities := []string{"ea", "eb", "ec"}

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		for _, id := range entities {
			id := id
			wg.Add(2)
			go func() {
				defer wg.Done()
				b.When(id)
			}()
			go func() {
				defer wg.Done()
				b.Forget(id)
			}()
		}
	}
	wg.Wait()
	// No assertion beyond no race / no panic.
}

// TestBackoff_ForgetUnknownEntity verifies that Forget on an unknown entity
// is a no-op (no panic).
func TestBackoff_ForgetUnknownEntity(t *testing.T) {
	b := newEntityBackoff(5*time.Millisecond, 1000*time.Second)
	require.NotPanics(t, func() {
		b.Forget("not-registered")
	})
}
