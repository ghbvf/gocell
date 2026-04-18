package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeGuardMiddleware returns a middleware that increments counter and delegates.
func makeGuardMiddleware(counter *atomic.Int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			counter.Add(1)
			next.ServeHTTP(w, r)
		})
	}
}

func TestWithInternalEndpointGuard_NilGuardFailsFast(t *testing.T) {
	// Run() must return an error when the guard is nil.
	// Validation happens at Step 0 (before any side effects), so no listener
	// or assembly start is needed — the error surfaces before net.Listen.
	asm := assembly.New(assembly.Config{ID: "guard-nil-test", DurabilityMode: cell.DurabilityDemo})

	b := New(
		WithAssembly(asm),
		WithHTTPAddr("127.0.0.1:0"),
		WithInternalEndpointGuard("/internal/v1/", nil),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := b.Run(ctx)
	require.Error(t, err, "nil guard must cause Run to fail")
	assert.Contains(t, err.Error(), "nil", "error must mention nil")
}

func TestWithInternalEndpointGuard_InvalidPrefix_FailsFast(t *testing.T) {
	// Prefix without leading slash must cause Run() to fail at Step 0.
	asm := assembly.New(assembly.Config{ID: "guard-prefix-test", DurabilityMode: cell.DurabilityDemo})
	guard := makeGuardMiddleware(new(atomic.Int64))

	b := New(
		WithAssembly(asm),
		WithHTTPAddr("127.0.0.1:0"),
		WithInternalEndpointGuard("internal/v1/", guard), // missing leading /
	)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := b.Run(ctx)
	require.Error(t, err, "prefix without leading slash must cause Run to fail")
	assert.Contains(t, err.Error(), "prefix")
}

func TestWithInternalEndpointGuard_PrefixMustEndWithSlash(t *testing.T) {
	// Prefix without trailing slash must cause Run() to fail at Step 0.
	asm := assembly.New(assembly.Config{ID: "guard-trailing-test", DurabilityMode: cell.DurabilityDemo})
	guard := makeGuardMiddleware(new(atomic.Int64))

	b := New(
		WithAssembly(asm),
		WithHTTPAddr("127.0.0.1:0"),
		WithInternalEndpointGuard("/internal/v1", guard), // missing trailing /
	)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	err := b.Run(ctx)
	require.Error(t, err, "prefix without trailing slash must cause Run to fail")
	assert.Contains(t, err.Error(), "prefix")
}

// routeRegisterCell registers an HTTP route at the given path so we can probe it.
type routeRegisterCell struct {
	*cell.BaseCell
	path    string
	handler http.HandlerFunc
}

func newRouteRegisterCell(id, path string, h http.HandlerFunc) *routeRegisterCell {
	return &routeRegisterCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{
			ID:   id,
			Type: cell.CellTypeCore,
		}),
		path:    path,
		handler: h,
	}
}

func (c *routeRegisterCell) RegisterRoutes(mux cell.RouteMux) {
	mux.Handle(c.path, c.handler)
}

func TestWithInternalEndpointGuard_Wiring(t *testing.T) {
	// A valid guard + prefix must:
	//   - wrap /internal/v1/* requests
	//   - NOT wrap /api/v1/* requests
	//   - NOT wrap /healthz
	ln := newLocalListener(t)

	var guardCount atomic.Int64
	guard := makeGuardMiddleware(&guardCount)

	var internalHit atomic.Int64
	var apiHit atomic.Int64

	asm := assembly.New(assembly.Config{ID: "guard-wiring-test", DurabilityMode: cell.DurabilityDemo})

	internalCell := newRouteRegisterCell("internal-cell", "/internal/v1/roles",
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			internalHit.Add(1)
			w.WriteHeader(http.StatusOK)
		}))
	apiCell := newRouteRegisterCell("api-cell", "/api/v1/users",
		http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			apiHit.Add(1)
			w.WriteHeader(http.StatusOK)
		}))

	require.NoError(t, asm.Register(internalCell))
	require.NoError(t, asm.Register(apiCell))

	b := New(
		WithAssembly(asm),
		WithListener(ln),
		WithShutdownTimeout(2*time.Second),
		WithInternalEndpointGuard("/internal/v1/", guard),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	addr := ln.Addr().String()
	require.Eventually(t, func() bool {
		resp, e := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
		if e != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "HTTP server did not become ready")

	// Hit /internal/v1/roles — guard must be called.
	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/internal/v1/roles", addr))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, int64(1), guardCount.Load(), "guard must be invoked for /internal/v1/* request")
	assert.Equal(t, int64(1), internalHit.Load())

	// Hit /api/v1/users — guard must NOT be called again.
	resp, err = testHTTPClient.Get(fmt.Sprintf("http://%s/api/v1/users", addr))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, int64(1), guardCount.Load(), "guard must NOT be invoked for /api/v1/* request")
	assert.Equal(t, int64(1), apiHit.Load())

	// Hit /healthz — guard must NOT be called again.
	resp, err = testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", addr))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, int64(1), guardCount.Load(), "guard must NOT be invoked for /healthz")

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}
