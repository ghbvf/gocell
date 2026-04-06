package config

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWatcher_OnChange(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(file, []byte("key: val1"), 0o644))

	w, err := NewWatcher(file)
	require.NoError(t, err)
	defer func() { _ = w.Close() }()

	var called atomic.Int32
	var lastEvent WatchEvent
	w.OnChange(func(evt WatchEvent) {
		lastEvent = evt
		called.Add(1)
	})

	w.Start()

	// Wait for the event loop to be ready instead of sleeping.
	select {
	case <-w.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not become ready in time")
	}

	// Modify the file.
	require.NoError(t, os.WriteFile(file, []byte("key: val2"), 0o644))

	// Wait for callback.
	assert.Eventually(t, func() bool {
		return called.Load() >= 1
	}, 2*time.Second, 50*time.Millisecond, "expected OnChange callback to fire")

	assert.Equal(t, file, lastEvent.Path, "WatchEvent.Path should be the watched file")
}

func TestWatcher_Close(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(file, []byte("key: val"), 0o644))

	w, err := NewWatcher(file)
	require.NoError(t, err)

	w.Start()
	err = w.Close()
	assert.NoError(t, err)

	// Double close should not panic.
	err = w.Close()
	assert.NoError(t, err)
}

func TestNewWatcher_InvalidPath(t *testing.T) {
	_, err := NewWatcher("/nonexistent/file.yaml")
	assert.Error(t, err)
}

func TestWatcher_StartWithContext(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(file, []byte("key: val"), 0o644))

	w, err := NewWatcher(file)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	w.StartWithContext(ctx)

	// Cancel the context, which should close the watcher.
	cancel()

	// Wait for the watcher to be closed.
	assert.Eventually(t, func() bool {
		// Try to close again — should be safe (idempotent).
		_ = w.Close()
		return true
	}, 2*time.Second, 50*time.Millisecond)
}
