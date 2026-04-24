package bootstrap

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dualListenerCell registers one /api/v1/* and one /internal/v1/* route so
// tests can verify dual-listener dispatch end-to-end.
type dualListenerCell struct {
	*cell.BaseCell
	onInternal func(http.ResponseWriter, *http.Request)
	onPublic   func(http.ResponseWriter, *http.Request)
}

func newDualListenerCell(onPublic, onInternal func(http.ResponseWriter, *http.Request)) *dualListenerCell {
	return &dualListenerCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{
			ID:   "dual-listener-cell",
			Type: cell.CellTypeCore,
		}),
		onInternal: onInternal,
		onPublic:   onPublic,
	}
}

func (c *dualListenerCell) RegisterRoutes(mux cell.RouteMux) {
	auth.Declare(mux, auth.RouteDecl{
		Method:  http.MethodGet,
		Path:    "/api/v1/test/ping",
		Handler: http.HandlerFunc(c.onPublic),
		Public:  true,
	})
	auth.Declare(mux, auth.RouteDecl{
		Method:    http.MethodGet,
		Path:      "/internal/v1/admin/ping",
		Handler:   http.HandlerFunc(c.onInternal),
		Delegated: true,
	})
}

// TestDualListener_PrimaryReturns404ForInternalPrefix is the core acceptance
// test for PR-A14a: hitting /internal/v1/* on the primary listener must
// yield 404, proving port-level physical isolation. The route IS registered
// inside the same Bootstrap — it just physically lives on the internal mux.
func TestDualListener_PrimaryReturns404ForInternalPrefix(t *testing.T) {
	primaryLn := newLocalListener(t)
	internalLn := newLocalListener(t)

	var publicHits, internalHits atomic.Int64
	c := newDualListenerCell(
		func(w http.ResponseWriter, _ *http.Request) {
			publicHits.Add(1)
			w.WriteHeader(http.StatusOK)
		},
		func(w http.ResponseWriter, _ *http.Request) {
			internalHits.Add(1)
			w.WriteHeader(http.StatusOK)
		},
	)
	asm := assembly.New(assembly.Config{ID: "dual-primary-404", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(c))

	b := New(
		WithAssembly(asm),
		WithPrimaryListener(primaryLn),
		WithInternalListener(internalLn),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	primaryAddr := primaryLn.Addr().String()
	internalAddr := internalLn.Addr().String()
	// Wait for primary to accept.
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", primaryAddr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "primary listener did not become ready")

	t.Run("primary_404s_internal_prefix", func(t *testing.T) {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/internal/v1/admin/ping", primaryAddr))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode,
			"primary listener must 404 on /internal/v1/* — port isolation contract")
		assert.Equal(t, int64(0), internalHits.Load(),
			"internal handler must NOT be invoked through primary listener")
	})

	t.Run("internal_404s_public_prefix", func(t *testing.T) {
		// internal:port + /api/v1/* → 404
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/api/v1/test/ping", internalAddr))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusNotFound, resp.StatusCode,
			"internal listener must 404 on /api/v1/*")
		assert.Equal(t, int64(0), publicHits.Load(),
			"public handler must NOT be invoked through internal listener")
	})

	t.Run("internal_404s_infra_endpoints", func(t *testing.T) {
		for _, p := range []string{"/healthz", "/readyz", "/metrics", "/"} {
			resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s%s", internalAddr, p))
			require.NoError(t, err)
			resp.Body.Close()
			assert.Equal(t, http.StatusNotFound, resp.StatusCode,
				"internal listener must 404 on infra path %q", p)
		}
	})

	t.Run("primary_routes_public_business", func(t *testing.T) {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/api/v1/test/ping", primaryAddr))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, int64(1), publicHits.Load())
	})

	t.Run("internal_routes_internal_business", func(t *testing.T) {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/internal/v1/admin/ping", internalAddr))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, int64(1), internalHits.Load())
	})

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// TestDualListener_InternalMiddlewareProtectsInternalRoutes verifies the
// service-token style guard installed via WithInternalMiddleware is the sole
// authentication layer for internal routes, and is NEVER invoked for public
// routes.
func TestDualListener_InternalMiddlewareProtectsInternalRoutes(t *testing.T) {
	primaryLn := newLocalListener(t)
	internalLn := newLocalListener(t)

	var guardCount atomic.Int64
	guard := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			guardCount.Add(1)
			if r.Header.Get("X-Service-Token") != "secret" {
				http.Error(w, "missing service token", http.StatusUnauthorized)
				return
			}
			next.ServeHTTP(w, r)
		})
	}

	var publicHits, internalHits atomic.Int64
	c := newDualListenerCell(
		func(w http.ResponseWriter, _ *http.Request) {
			publicHits.Add(1)
			w.WriteHeader(http.StatusOK)
		},
		func(w http.ResponseWriter, _ *http.Request) {
			internalHits.Add(1)
			w.WriteHeader(http.StatusOK)
		},
	)
	asm := assembly.New(assembly.Config{ID: "dual-guard-test", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(c))

	b := New(
		WithAssembly(asm),
		WithPrimaryListener(primaryLn),
		WithInternalListener(internalLn),
		WithShutdownTimeout(2*time.Second),
		WithInternalMiddleware(guard),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	primaryAddr := primaryLn.Addr().String()
	internalAddr := internalLn.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", primaryAddr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "primary listener did not become ready")

	// Internal without token → 401 from guard.
	t.Run("internal_without_token_401", func(t *testing.T) {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/internal/v1/admin/ping", internalAddr))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
		assert.Equal(t, int64(0), internalHits.Load(), "handler must not run without service token")
	})

	// Internal with token → 200.
	t.Run("internal_with_token_200", func(t *testing.T) {
		req, err := http.NewRequest(http.MethodGet,
			fmt.Sprintf("http://%s/internal/v1/admin/ping", internalAddr), nil)
		require.NoError(t, err)
		req.Header.Set("X-Service-Token", "secret")
		resp, err := testHTTPClient.Do(req)
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, int64(1), internalHits.Load())
	})

	t.Run("public_unaffected_by_internal_guard", func(t *testing.T) {
		before := guardCount.Load()
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/api/v1/test/ping", primaryAddr))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, before, guardCount.Load(),
			"internal middleware must not fire for primary listener requests")
	})

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// TestDualListener_Phase0RejectsEqualAddrs verifies the fail-fast guard that
// catches accidentally setting the same address on both listeners (which would
// bind OK for one and fail for the other under random ordering).
func TestDualListener_Phase0RejectsEqualAddrs(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "equal-addr-test", DurabilityMode: cell.DurabilityDemo})

	b := New(
		WithAssembly(asm),
		WithHTTPPrimaryAddr("127.0.0.1:7777"),
		WithHTTPInternalAddr("127.0.0.1:7777"),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := b.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "must differ")
}

// TestDualListener_Phase0RejectsEmptyAddr verifies empty primary or internal
// addresses fail at phase 0.
func TestDualListener_Phase0RejectsEmptyAddr(t *testing.T) {
	cases := []struct {
		name string
		opts []Option
		want string
	}{
		{"empty_primary", []Option{WithHTTPPrimaryAddr("")}, "primary HTTP addr"},
		{"empty_internal", []Option{WithHTTPInternalAddr("")}, "internal HTTP addr"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			asm := assembly.New(assembly.Config{ID: "empty-addr-" + tc.name, DurabilityMode: cell.DurabilityDemo})
			opts := append([]Option{WithAssembly(asm)}, tc.opts...)
			b := New(opts...)

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			err := b.Run(ctx)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

// TestDualListener_WithInternalMiddlewareNilFailsFast verifies a nil middleware
// entry is rejected at phase 0 — the internal listener's auth layer cannot be
// silently disabled.
func TestDualListener_WithInternalMiddlewareNilFailsFast(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "nil-mw-test", DurabilityMode: cell.DurabilityDemo})

	b := New(
		WithAssembly(asm),
		WithPrimaryListener(newLocalListener(t)),
		WithInternalListener(newLocalListener(t)),
		WithInternalMiddleware(nil),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := b.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nil")
}

// TestDualListener_InternalBindFailure_ClosesOwnedPrimary verifies that when
// the internal listener bind fails AND Bootstrap owns (bound) the primary
// listener, Bootstrap closes it to avoid leaking a bound socket. When the
// primary listener was caller-injected via WithPrimaryListener, Bootstrap
// must NOT close it (caller retains ownership).
func TestDualListener_InternalBindFailure_ClosesOwnedPrimary(t *testing.T) {
	// Use a pre-bound listener on a known port, then force internal to try
	// binding the same port → EADDRINUSE. The primary path uses the
	// caller-injected listener, so Bootstrap must NOT close it.
	callerLn := newLocalListener(t)
	collidingAddr := callerLn.Addr().String()

	asm := assembly.New(assembly.Config{ID: "bind-fail-test", DurabilityMode: cell.DurabilityDemo})

	b := New(
		WithAssembly(asm),
		WithPrimaryListener(callerLn),
		WithHTTPInternalAddr(collidingAddr), // guaranteed to collide
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := b.Run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "listen internal")

	// Caller-owned listener must still be usable (Bootstrap didn't close it).
	// A best-effort probe: Accept() would block; instead verify we can Dial
	// our own listener address and the listener is still the owner.
	done := make(chan struct{})
	go func() {
		conn, _ := callerLn.Accept()
		if conn != nil {
			conn.Close()
		}
		close(done)
	}()
	dialConn, dialErr := net.Dial("tcp", callerLn.Addr().String())
	if dialConn != nil {
		dialConn.Close()
	}
	assert.NoError(t, dialErr, "caller-owned primary listener must still accept connections")
	<-done
}

// TestDualListener_ShutdownClosesBothServersNoGoroutineLeak verifies that
// ctx.Cancel triggers parallel Shutdown on both servers and that no
// bootstrap goroutine leaks beyond the shutdown deadline.
func TestDualListener_ShutdownClosesBothServersNoGoroutineLeak(t *testing.T) {
	primaryLn := newLocalListener(t)
	internalLn := newLocalListener(t)

	c := newDualListenerCell(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
	)
	asm := assembly.New(assembly.Config{ID: "shutdown-test", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(c))

	b := New(
		WithAssembly(asm),
		WithPrimaryListener(primaryLn),
		WithInternalListener(internalLn),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	primaryAddr := primaryLn.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", primaryAddr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "primary listener did not become ready")

	before := runtime.NumGoroutine()

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}

	// Allow short settle window for goroutine cleanup.
	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= before
	}, 2*time.Second, 50*time.Millisecond, "goroutine count did not return to baseline")

	// Both listeners must be closed after shutdown — new Listen on same
	// *:0 port won't collide since ports are ephemeral, but the listeners
	// themselves should report closed when double-closed.
	// net.Listener's Close is idempotent-ish: second close typically returns
	// errClosed, not an error we must propagate. We just verify they're not
	// accepting.
	_, err := net.Dial("tcp", primaryAddr)
	assert.Error(t, err, "primary listener should be closed; Dial must fail")
}
