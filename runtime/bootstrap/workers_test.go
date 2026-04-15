package bootstrap

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/http/router"
	"github.com/ghbvf/gocell/runtime/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// countWorker is a minimal worker that tracks start calls.
type countWorker struct {
	started int
}

func (w *countWorker) Start(ctx context.Context) error {
	w.started++
	<-ctx.Done()
	return nil
}

func (w *countWorker) Stop(_ context.Context) error { return nil }

func TestWithWorkers(t *testing.T) {
	w := &countWorker{}
	b := New(WithWorkers(w))
	assert.Len(t, b.workers, 1)
}

func TestWithRouterOptions(t *testing.T) {
	opt := router.WithBodyLimit(512)
	b := New(WithRouterOptions(opt))
	assert.Len(t, b.routerOpts, 1)
}

func TestRun_WithWorkers_Shutdown(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	asm := assembly.New(assembly.Config{ID: "test-workers", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(newTestCell("cell-1")))

	w := &countWorker{}

	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
		WithWorkers(w),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	// Wait for HTTP server to become ready instead of sleeping.
	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get("http://" + addr + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return true
	}, 3*time.Second, 50*time.Millisecond, "HTTP server did not become ready")
	cancel()

	select {
	case runErr := <-done:
		assert.NoError(t, runErr)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// Compile-time check that countWorker satisfies worker.Worker interface.
var _ worker.Worker = (*countWorker)(nil)
