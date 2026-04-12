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

// TestWatcher_AtomicReplace_RenameCreate simulates Kubernetes ConfigMap atomic
// replace: rename old file, then create a new file with the same name.
func TestWatcher_AtomicReplace_RenameCreate(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(file, []byte("key: v1"), 0o644))

	w, err := NewWatcher(file)
	require.NoError(t, err)
	defer func() { _ = w.Close() }()

	var called atomic.Int32
	w.OnChange(func(_ WatchEvent) { called.Add(1) })
	w.Start()

	select {
	case <-w.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("watcher not ready")
	}

	// Atomic replace: rename → create.
	require.NoError(t, os.Rename(file, file+".bak"))
	require.NoError(t, os.WriteFile(file, []byte("key: v2"), 0o644))

	assert.Eventually(t, func() bool {
		return called.Load() >= 1
	}, 3*time.Second, 50*time.Millisecond, "expected callback after atomic rename+create")
}

// TestWatcher_AtomicReplace_RemoveRecreate verifies that remove followed by
// recreate still fires the callback (common in container orchestrators).
func TestWatcher_AtomicReplace_RemoveRecreate(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(file, []byte("key: v1"), 0o644))

	w, err := NewWatcher(file)
	require.NoError(t, err)
	defer func() { _ = w.Close() }()

	var called atomic.Int32
	w.OnChange(func(_ WatchEvent) { called.Add(1) })
	w.Start()

	select {
	case <-w.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("watcher not ready")
	}

	// Remove + recreate.
	require.NoError(t, os.Remove(file))
	require.NoError(t, os.WriteFile(file, []byte("key: v2"), 0o644))

	assert.Eventually(t, func() bool {
		return called.Load() >= 1
	}, 3*time.Second, 50*time.Millisecond, "expected callback after remove+recreate")
}

// TestWatcher_IgnoresUnrelatedFiles verifies that changes to other files in
// the same directory do NOT fire the callback.
func TestWatcher_IgnoresUnrelatedFiles(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "config.yaml")
	other := filepath.Join(dir, "other.yaml")
	require.NoError(t, os.WriteFile(file, []byte("key: v1"), 0o644))

	w, err := NewWatcher(file)
	require.NoError(t, err)
	defer func() { _ = w.Close() }()

	var called atomic.Int32
	w.OnChange(func(_ WatchEvent) { called.Add(1) })
	w.Start()

	select {
	case <-w.Ready():
	case <-time.After(2 * time.Second):
		t.Fatal("watcher not ready")
	}

	// Write to an unrelated file.
	require.NoError(t, os.WriteFile(other, []byte("unrelated: true"), 0o644))

	// Give enough time for a spurious event to be delivered.
	time.Sleep(500 * time.Millisecond)
	assert.Equal(t, int32(0), called.Load(), "unrelated file change must not fire callback")
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
