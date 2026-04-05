package config

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/fsnotify/fsnotify"
)

// WatchEvent carries information about a file change detected by the Watcher.
type WatchEvent struct {
	Path string // Path of the changed file.
}

// Watcher monitors a file for changes and invokes registered callbacks.
type Watcher struct {
	path      string
	watcher   *fsnotify.Watcher
	callbacks []func(WatchEvent)
	mu        sync.Mutex
	done      chan struct{}
}

// NewWatcher creates a Watcher for the given file path. The watcher does not
// start until Start is called.
func NewWatcher(path string) (*Watcher, error) {
	fw, err := fsnotify.NewWatcher()
	if err != nil {
		return nil, fmt.Errorf("config: new watcher: %w", err)
	}
	if err := fw.Add(path); err != nil {
		_ = fw.Close()
		return nil, fmt.Errorf("config: watch path %s: %w", path, err)
	}
	return &Watcher{
		path:    path,
		watcher: fw,
		done:    make(chan struct{}),
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

func (w *Watcher) loop() {
	for {
		select {
		case event, ok := <-w.watcher.Events:
			if !ok {
				return
			}
			if event.Has(fsnotify.Write) || event.Has(fsnotify.Create) {
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

// Close stops the watcher and releases resources.
func (w *Watcher) Close() error {
	select {
	case <-w.done:
		// Already closed.
	default:
		close(w.done)
	}
	return w.watcher.Close()
}
