package configcore

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/outbox"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

// TestConfigCore_AfterStart_StartsTombstoneGC verifies that AfterStart launches
// the tombstone-GC goroutine (which creates a ticker), and that BeforeStop
// drains it cleanly. Also asserts BeforeStop idempotency.
func TestConfigCore_AfterStart_StartsTombstoneGC(t *testing.T) {
	fc := clockmock.New(time.Unix(0, 0))
	ctx := context.Background()

	c := NewConfigCore(
		WithClock(fc),
		WithTombstoneTTL(24*time.Hour),
		WithEventbusCacheCollector(obmetrics.NoopEventbusCacheCollector{}),
		WithInMemoryDefaults(),
		WithEmitter(outbox.NewNoopEmitter()),
	)
	require.NoError(t, c.Init(ctx, newTestRecorder()))
	require.NoError(t, c.Start(ctx))
	require.NoError(t, c.AfterStart(ctx))

	// The GC goroutine creates a ticker asynchronously — wait for it to register.
	assert.Eventually(t, func() bool {
		return fc.PendingTickers() >= 1
	}, 2*time.Second, 5*time.Millisecond,
		"GC goroutine must create a ticker after AfterStart")

	// BeforeStop drains the goroutine.
	require.NoError(t, c.BeforeStop(ctx))

	// Idempotency: a second call must return nil.
	require.NoError(t, c.BeforeStop(ctx))

	require.NoError(t, c.Stop(ctx))
}

// TestConfigCore_LifecycleHooks_InterfaceSatisfied verifies at runtime that
// *ConfigCore implements both optional lifecycle interfaces discovered by the
// assembly via type assertion.
func TestConfigCore_LifecycleHooks_InterfaceSatisfied(t *testing.T) {
	c := NewConfigCore(WithClock(clockmock.New(time.Unix(0, 0))), WithInMemoryDefaults())

	_, ok := any(c).(cell.AfterStarter)
	assert.True(t, ok, "*ConfigCore must implement cell.AfterStarter")

	_, ok = any(c).(cell.BeforeStopper)
	assert.True(t, ok, "*ConfigCore must implement cell.BeforeStopper")
}

// TestConfigCore_BeforeStop_SafeWhenGCNeverStarted verifies that calling
// BeforeStop without a preceding AfterStart is safe (idempotent, nil error).
func TestConfigCore_BeforeStop_SafeWhenGCNeverStarted(t *testing.T) {
	fc := clockmock.New(time.Unix(0, 0))
	ctx := context.Background()

	c := NewConfigCore(
		WithClock(fc),
		WithInMemoryDefaults(),
		WithEmitter(outbox.NewNoopEmitter()),
	)
	require.NoError(t, c.Init(ctx, newTestRecorder()))
	require.NoError(t, c.Start(ctx))
	// AfterStart deliberately NOT called.
	require.NoError(t, c.BeforeStop(ctx))
	require.NoError(t, c.Stop(ctx))
}
