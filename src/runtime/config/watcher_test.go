package config

import (
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
	w.OnChange(func() {
		called.Add(1)
	})

	w.Start()

	// Give the watcher time to start.
	time.Sleep(50 * time.Millisecond)

	// Modify the file.
	require.NoError(t, os.WriteFile(file, []byte("key: val2"), 0o644))

	// Wait for callback.
	assert.Eventually(t, func() bool {
		return called.Load() >= 1
	}, 2*time.Second, 50*time.Millisecond, "expected OnChange callback to fire")
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
