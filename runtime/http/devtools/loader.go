package devtools

import (
	"context"
	"sync/atomic"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
)

// LoadFunc is the signature of a package-dep loader function. It is injected
// at construction time so that loader.go (runtime/) does not import
// tools/depgraph directly — tools/ is outside the LAYER-03 allowed set for
// runtime/ packages. The composition root (cmd/) injects a closure that calls
// tools/depgraph.Load.
//
// The function must return a non-nil *metadata.PackageDepsView regardless of
// whether loading succeeded or failed. Status field must be "ready" or "error".
// ctx cancellation is honored: a canceled context should return promptly with
// Status="error".
type LoadFunc func(ctx context.Context, root string) *metadata.PackageDepsView

// PackageDepLoader loads the package-level dependency graph lazily in a
// background goroutine. The current view is accessible at any time via View(),
// which never blocks.
//
// Zero value is invalid; use NewPackageDepLoader.
type PackageDepLoader struct {
	state  atomic.Value // holds *metadata.PackageDepsView; never nil after construction
	cancel context.CancelFunc
	done   chan struct{}
}

// NewPackageDepLoader constructs a PackageDepLoader and starts a background
// goroutine that calls fn(ctx, root). The context passed to fn is a child of
// ctx that is canceled when Close is called.
//
// The initial View() returns &PackageDepsView{Status:"loading"}.
// On completion, View() returns the result returned by fn.
//
// clk is accepted for API symmetry with other devtools constructors; the
// loader itself does not use wall-clock time.
func NewPackageDepLoader(ctx context.Context, root string, _ clock.Clock, fn LoadFunc) *PackageDepLoader {
	childCtx, cancel := context.WithCancel(ctx)
	l := &PackageDepLoader{
		cancel: cancel,
		done:   make(chan struct{}),
	}
	l.state.Store(&metadata.PackageDepsView{Status: "loading"})

	go func() {
		defer close(l.done)
		result := fn(childCtx, root)
		if result == nil {
			result = &metadata.PackageDepsView{Status: "error", Error: "loader returned nil"}
		}
		l.state.Store(result)
	}()

	return l
}

// View returns a snapshot of the current load state. Never blocks; never
// returns nil. The caller must not modify the returned value.
func (l *PackageDepLoader) View() *metadata.PackageDepsView {
	v, _ := l.state.Load().(*metadata.PackageDepsView)
	return v
}

// Close cancels the background load and waits for the goroutine to exit.
// Safe to call multiple times; subsequent calls return nil immediately.
func (l *PackageDepLoader) Close() error {
	l.cancel()
	<-l.done
	return nil
}
