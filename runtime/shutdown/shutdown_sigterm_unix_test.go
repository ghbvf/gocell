//go:build unix

package shutdown

import (
	"context"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestManager_Wait_SIGTERM(t *testing.T) {
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

	// Send SIGTERM to self.
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGTERM)

	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(3 * time.Second):
		t.Fatal("Wait did not return after SIGTERM")
	}

	assert.True(t, hookCalled.Load())
}
