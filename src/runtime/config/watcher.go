// Package config — watcher.go
//
// ref: spf13/viper viper.go — WatchConfig watches the parent directory for
// atomic saves and renames; fsnotify docs recommend directory-level watch.
// Adopted: watch filepath.Dir(path), filter by filepath.Base(path).
// Deviated from go-micro (file-level watch with Rename re-add): directory-level
// is more robust for Kubernetes ConfigMap symlink swaps.
package config

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// WatchEvent carries information about a file change detected by the Watcher.
type WatchEvent struct {
	Path string // Path of the changed file.
}

// Watcher monitors a file for changes by watching its parent directory.
// This correctly handles atomic replace (rename+create), remove+recreate,
// and Kubernetes ConfigMap symlink swaps where file-level inotify/kqueue
// watches would silently break.
type Watcher struct {
	path       string // original path (reported in WatchEvent)
	dir        string // parent directory being watched
	targetName string // base filename to filter events
	watcher    *fsnotify.Watcher
	callbacks  []func(WatchEvent)
	mu         sync.Mutex
	done       chan struct{}
	ready      chan struct{} // closed when the event loop starts
	closeOnce  sync.Once
	readyOnce  sync.Once
}

// NewWatcher creates a Watcher for the given file path. The watcher monitors
// the parent directory and filters events for the target filename.
// The watcher does not start until Start is called.
func NewWatcher(path string) (*Watcher, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("config: abs path %s: %w", path, err)
	}

	dir := filepath.Dir(absPath)
	targetName := filepath.Base(absPath)

	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("config: new watcher: %w", err)
	}
	if err := fw.Add(dir); err != nil {
		_ = fw.Close()
		return nil, fmt.Errorf("config: watch dir %s: %w", dir, err)
	}
	return &Watcher{
		path:       absPath,
		dir:        dir,
		targetName: targetName,
		watcher:    fw,
		done:       make(chan struct{}),
		ready:      make(chan struct{}),
	}, nil
}

// OnChange registers a callback that fires when the watched file changes.
func (w *Watcher) OnChange(fn func(WatchEvent)) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.callbacks = append(w.callbacks, fn)
}

// Start begins watching for file changes in a goroutine. It blocks until
// Close is called or an unrecoverable error occurs.
func (w *Watcher) Start() {
	go w.loop()
}

// StartWithContext begins watching using the provided context. When ctx is
// cancelled, the watcher is closed automatically. This allows the watcher
// to be tied to a parent shutdown context.
func (w *Watcher) StartWithContext(ctx context.Context) {
	go func() {
		<-ctx.Done()
		_ = w.Close()
	}()
	go w.loop()
}

// Ready returns a channel that is closed when the event loop has started and is
// ready to process file-system events. Useful in tests to avoid time.Sleep.
func (w *Watcher) Ready() <-chan struct{} {
	return w.ready
}

func (w *Watcher) loop() {
	w.readyOnce.Do(func() { close(w.ready) })
	for {
		select {
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			// Filter: only react to events on our target file.
			if filepath.Base(event.Name) != w.targetName {
				continue
			}
			// Write and Create indicate new content (including atomic replace).
			// Rename and Remove alone do not fire callbacks — we wait for the
			// subsequent Create that carries the new data.
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
				w.fireCallbacks()
			}
		case err, ok := <-w.watcher.Errors:
			if !ok {
				return
			}
			slog.Error("config watcher error", slog.Any("error", err))
		case <-w.done:
			return
		}
	}
}

func (w *Watcher) fireCallbacks() {
	w.mu.Lock()
	cbs := make([]func(WatchEvent), len(w.callbacks))
	copy(cbs, w.callbacks)
	w.mu.Unlock()

	evt := WatchEvent{Path: w.path}
	for _, fn := range cbs {
		func() {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("config watcher callback panic", slog.Any("panic", r))
				}
			}()
			fn(evt)
		}()
	}
}

// Close stops the watcher and releases resources. It is safe to call
// concurrently from multiple goroutines.
func (w *Watcher) Close() error {
	w.closeOnce.Do(func() {
		close(w.done)
	})
	return w.watcher.Close()
}
