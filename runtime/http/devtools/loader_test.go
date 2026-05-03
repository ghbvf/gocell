package devtools_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/runtime/http/devtools"
)

const (
	loaderTestPollTimeout  = 5 * time.Second
	loaderTestPollInterval = 5 * time.Millisecond
)

// slowLoadFunc returns a LoadFunc that blocks on ready before returning result.
func slowLoadFunc(ready <-chan struct{}, result *metadata.PackageDepsView) devtools.LoadFunc {
	return func(_ context.Context, _ string) *metadata.PackageDepsView {
		<-ready
		return result
	}
}

// immediateLoadFunc returns a LoadFunc that returns result immediately.
func immediateLoadFunc(result *metadata.PackageDepsView) devtools.LoadFunc {
	return func(_ context.Context, _ string) *metadata.PackageDepsView {
		return result
	}
}

// blockingLoadFunc returns a LoadFunc that blocks until ctx is canceled.
func blockingLoadFunc() devtools.LoadFunc {
	return func(ctx context.Context, _ string) *metadata.PackageDepsView {
		<-ctx.Done()
		return &metadata.PackageDepsView{Status: "error", Error: "load canceled"}
	}
}

func TestLoader_InitialLoading(t *testing.T) {
	t.Parallel()

	ready := make(chan struct{})
	loader := devtools.NewPackageDepLoader(
		context.Background(),
		"/tmp/fake-root",
		clock.Real(),
		slowLoadFunc(ready, &metadata.PackageDepsView{Status: "ready"}),
	)
	t.Cleanup(func() {
		close(ready)
		if err := loader.Close(); err != nil {
			t.Errorf("loader.Close: %v", err)
		}
	})

	view := loader.View()
	if view == nil {
		t.Fatal("View() returned nil immediately after construction")
	}
	if view.Status != "loading" {
		t.Errorf("initial status = %q, want %q", view.Status, "loading")
	}
}

func TestLoader_LoadSuccess(t *testing.T) {
	t.Parallel()

	successView := &metadata.PackageDepsView{Status: "ready"}
	loader := devtools.NewPackageDepLoader(
		context.Background(),
		"/tmp/fake-root",
		clock.Real(),
		immediateLoadFunc(successView),
	)
	t.Cleanup(func() {
		if err := loader.Close(); err != nil {
			t.Errorf("loader.Close: %v", err)
		}
	})

	require.Eventually(t, func() bool {
		v := loader.View()
		if v.Status == "error" {
			t.Fatalf("unexpected error: %s", v.Error)
		}
		return v.Status == "ready"
	}, loaderTestPollTimeout, loaderTestPollInterval, "loader never reached status=ready")
}

func TestLoader_LoadError(t *testing.T) {
	t.Parallel()

	errorView := &metadata.PackageDepsView{Status: "error", Error: "no go module found"}
	loader := devtools.NewPackageDepLoader(
		context.Background(),
		"/nonexistent/path/that/is/not/a/gomod",
		clock.Real(),
		immediateLoadFunc(errorView),
	)
	t.Cleanup(func() {
		if err := loader.Close(); err != nil {
			t.Errorf("loader.Close: %v", err)
		}
	})

	require.Eventually(t, func() bool {
		v := loader.View()
		if v.Status == "ready" {
			t.Fatal("unexpected ready status for non-module path")
		}
		if v.Status == "error" {
			if v.Error == "" {
				t.Error("error status should have non-empty Error field")
			}
			return true
		}
		return false
	}, loaderTestPollTimeout, loaderTestPollInterval, "loader never reached status=error")
}

func TestLoader_ConcurrentReaders(t *testing.T) {
	t.Parallel()

	successView := &metadata.PackageDepsView{Status: "ready"}
	loader := devtools.NewPackageDepLoader(
		context.Background(),
		"/tmp/fake-root",
		clock.Real(),
		immediateLoadFunc(successView),
	)
	t.Cleanup(func() {
		if err := loader.Close(); err != nil {
			t.Errorf("loader.Close: %v", err)
		}
	})

	const numReaders = 100
	var wg sync.WaitGroup
	wg.Add(numReaders)
	for range numReaders {
		go func() {
			defer wg.Done()
			v := loader.View()
			if v == nil {
				t.Errorf("View() returned nil")
			}
		}()
	}
	wg.Wait()
}

func TestLoader_Close(t *testing.T) {
	t.Parallel()

	loader := devtools.NewPackageDepLoader(
		context.Background(),
		"/tmp/fake-root",
		clock.Real(),
		blockingLoadFunc(),
	)

	done := make(chan error, 1)
	go func() {
		done <- loader.Close()
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Close() returned error: %v", err)
		}
	case <-time.After(loaderTestPollTimeout):
		t.Fatal("Close() did not return within 5s — goroutine may be leaked")
	}
}
