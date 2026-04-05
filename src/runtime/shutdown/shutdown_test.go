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

func TestManager_Shutdown_RunsHooks(t *testing.T) {
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

	err := m.Shutdown()
	require.NoError(t, err)
	assert.Equal(t, []int{1, 2}, order)
}

func TestManager_Shutdown_HookError(t *testing.T) {
	m := New(WithTimeout(5 * time.Second))

	expectedErr := errors.New("hook failed")
	m.Register(func(ctx context.Context) error {
		return expectedErr
	})

	var secondCalled atomic.Bool
	m.Register(func(ctx context.Context) error {
		secondCalled.Store(true)
		return nil
	})

	err := m.Shutdown()
	assert.ErrorIs(t, err, expectedErr)
	// Second hook should not be called because first failed.
	assert.False(t, secondCalled.Load())
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
