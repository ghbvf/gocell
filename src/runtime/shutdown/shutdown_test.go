package shutdown

import (
	"context"
	"errors"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestManager_Shutdown_RunsHooksLIFO(t *testing.T) {
	m := New(WithTimeout(5 * time.Second))

	var order []int
	m.Register(func(ctx context.Context) error {
		order = append(order, 1)
		return nil
	})
	m.Register(func(ctx context.Context) error {
		order = append(order, 2)
		return nil
	})
	m.Register(func(ctx context.Context) error {
		order = append(order, 3)
		return nil
	})

	err := m.Shutdown()
	require.NoError(t, err)
	// LIFO: last registered (3) runs first, then 2, then 1.
	assert.Equal(t, []int{3, 2, 1}, order)
}

func TestManager_Shutdown_HookErrorContinues(t *testing.T) {
	m := New(WithTimeout(5 * time.Second))

	var firstCalled atomic.Bool
	m.Register(func(ctx context.Context) error {
		firstCalled.Store(true)
		return nil
	})

	expectedErr := errors.New("hook failed")
	m.Register(func(ctx context.Context) error {
		return expectedErr
	})

	// LIFO: hook 2 (fail) runs first, then hook 1 should still run.
	err := m.Shutdown()
	assert.ErrorIs(t, err, expectedErr)
	// First hook should still be called despite second (run first in LIFO) failing.
	assert.True(t, firstCalled.Load(), "remaining hooks must execute even after a failure")
}

func TestManager_Shutdown_Timeout(t *testing.T) {
	m := New(WithTimeout(50 * time.Millisecond))

	m.Register(func(ctx context.Context) error {
		// Respect the context deadline.
		<-ctx.Done()
		return ctx.Err()
	})

	err := m.Shutdown()
	assert.Error(t, err)
}

func TestManager_Shutdown_AllHooksRunOnError(t *testing.T) {
	m := New(WithTimeout(5 * time.Second))

	var order []int
	m.Register(func(ctx context.Context) error {
		order = append(order, 1)
		return nil
	})
	m.Register(func(ctx context.Context) error {
		order = append(order, 2)
		return errors.New("hook 2 failed")
	})
	m.Register(func(ctx context.Context) error {
		order = append(order, 3)
		return nil
	})

	err := m.Shutdown()
	// Hook 2 fails, but hook 1 should still run.
	assert.Error(t, err)
	// LIFO: 3, 2, 1 — all three should be called.
	assert.Equal(t, []int{3, 2, 1}, order)
}

func TestManager_DefaultTimeout(t *testing.T) {
	m := New()
	assert.Equal(t, DefaultTimeout, m.timeout)
}

func TestManager_NoHooks(t *testing.T) {
	m := New(WithTimeout(100 * time.Millisecond))
	err := m.Shutdown()
	// With no hooks and a fresh context, there should be no error.
	assert.NoError(t, err)
}

func TestManager_Wait_Signal(t *testing.T) {
	m := New(WithTimeout(5 * time.Second))

	var hookCalled atomic.Bool
	m.Register(func(ctx context.Context) error {
		hookCalled.Store(true)
		return nil
	})

	done := make(chan error, 1)
	go func() {
		done <- m.Wait()
	}()

	// Give Wait time to set up the signal handler.
	time.Sleep(50 * time.Millisecond)

	// Send SIGINT to self.
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGINT)

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not return after signal")
	}

	assert.True(t, hookCalled.Load())
}
