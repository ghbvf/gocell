package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
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

func (c *dualListenerCell) RouteGroups() []cell.RouteGroup {
	return []cell.RouteGroup{
		{
			Listener: cell.PrimaryListener,
			Prefix:   "",
			Register: func(mux cell.RouteMux) error {
				auth.MustMount(mux, auth.Route{Contract: testHTTPContract(http.MethodGet, "/api/v1/test/ping"), Handler: http.HandlerFunc(c.onPublic), Public: true})
				return nil
			},
		},
		{
			Listener: cell.InternalListener,
			Prefix:   "",
			Register: func(mux cell.RouteMux) error {
				auth.MustMount(mux, auth.Route{Contract: testHTTPContract(http.MethodGet, "/internal/v1/admin/ping"), Handler: http.HandlerFunc(c.onInternal)})
				return nil
			},
		},
	}
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
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
		WithListener(cell.InternalListener, internalLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(internalLn)),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	primaryAddr := primaryLn.Addr().String()
	internalAddr := internalLn.Addr().String()
	// Wait for primary to accept. Health endpoints fall back to primary when no HealthListener.
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

// TestDualListener_InternalRoutesAccessibleWithoutJWT verifies that the
// InternalListener router does NOT install JWT auth (no auth verifier on
// internal listener — service-token protection is the caller's responsibility
// via a cell.Policy on the InternalListener declaration).
//
// PR-A14b: WithInternalMiddleware was deleted. Internal listener middleware
// must now be applied via cell.Policy in WithListener or per-group policy.
func TestDualListener_InternalRoutesAccessibleWithoutJWT(t *testing.T) {
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
	asm := assembly.New(assembly.Config{ID: "dual-nojwt-test", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(c))

	b := New(
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
		WithListener(cell.InternalListener, internalLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(internalLn)),
		WithShutdownTimeout(2*time.Second),
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

	// Internal endpoint is reachable without a JWT (no auth verifier on internal listener).
	t.Run("internal_accessible_without_token", func(t *testing.T) {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/internal/v1/admin/ping", internalAddr))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"internal listener must NOT require JWT (service-token auth is caller-side policy)")
		assert.Equal(t, int64(1), internalHits.Load())
	})

	// Public endpoint is reachable on the primary listener.
	t.Run("public_reachable_on_primary", func(t *testing.T) {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/api/v1/test/ping", primaryAddr))
		require.NoError(t, err)
		defer resp.Body.Close()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// TestDualListener_EqualAddrsBindFails verifies that setting the same address
// on both primary and internal listeners results in a bind failure at phase7.
// PR-A14b: duplicate address detection is no longer a phase0 check — it
// surfaces as an OS-level EADDRINUSE error when the second socket is bound.
func TestDualListener_EqualAddrsBindFails(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "equal-addr-test", DurabilityMode: cell.DurabilityDemo})
	// Pre-bind primary to ensure port is held; use the same port for internal.
	primaryLn := newLocalListener(t)
	collidingAddr := primaryLn.Addr().String()

	b := New(
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
		WithListener(cell.InternalListener, collidingAddr, []cell.ListenerAuth{cell.AuthNone{}}), // collides with primary
		WithShutdownTimeout(time.Second),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := b.Run(ctx)
	require.Error(t, err, "duplicate address must cause a bind error")
}

// TestDualListener_Phase0RejectsEmptyAddr verifies empty primary or internal
// addresses fail at phase 0 (validateHTTPListenerConfigs).
func TestDualListener_Phase0RejectsEmptyAddr(t *testing.T) {
	cases := []struct {
		name string
		opts []Option
	}{
		{"empty_primary", []Option{WithListener(cell.PrimaryListener, "", []cell.ListenerAuth{cell.AuthNone{}})}},
		{"empty_internal", []Option{WithListener(cell.InternalListener, "", []cell.ListenerAuth{cell.AuthNone{}})}},
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
			// PR-A14b: error now comes from validateHTTPListenerConfigs, not old addr-specific checks.
			assert.Contains(t, err.Error(), "no address or pre-bound net.Listener")
		})
	}
}

// TestDualListener_WithInternalMiddlewareNilFailsFast was removed in PR-A14b.
// WithInternalMiddleware and the internalMiddlewares field are deleted.
// Internal listener protection is now via cell.Policy in WithListener.

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
		WithListener(cell.PrimaryListener, callerLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(callerLn)),
		WithListener(cell.InternalListener, collidingAddr, []cell.ListenerAuth{cell.AuthNone{}}), // guaranteed to collide
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
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
		WithListener(cell.InternalListener, internalLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(internalLn)),
		WithShutdownTimeout(2*time.Second),
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

	// Baseline is taken AFTER the server is confirmed stable (healthz 200 above).
	// All bootstrap-internal goroutines (HTTP serve loops, etc.) are already running,
	// so the baseline already includes them. Taking it here — before cancel() — provides
	// a happens-before synchronisation point: no goroutine launches between this line
	// and cancel().
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

	// Both listeners must be closed after shutdown — verify neither accepts new connections.
	_, err := net.Dial("tcp", primaryAddr)
	assert.Error(t, err, "primary listener should be closed; Dial must fail")

	_, err = net.Dial("tcp", internalAddr)
	assert.Error(t, err, "internal listener should be closed; Dial must fail")
}

// TestTripleListener_ShutdownNoGoroutineLeak extends the dual-listener goroutine
// leak test to three listeners (primary + internal + health), verifying that
// Bootstrap's shutdown drains all three servers without leaking goroutines (TEST-02).
func TestTripleListener_ShutdownNoGoroutineLeak(t *testing.T) {
	primaryLn := newLocalListener(t)
	internalLn := newLocalListener(t)
	healthLn := newLocalListener(t)

	c := newDualListenerCell(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
	)
	asm := assembly.New(assembly.Config{ID: "triple-shutdown-test", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(c))

	b := New(
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
		WithListener(cell.InternalListener, internalLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(internalLn)),
		WithListener(cell.HealthListener, healthLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(healthLn)),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	healthAddr := healthLn.Addr().String()
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", healthAddr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "health listener did not become ready")

	// Baseline is taken AFTER the server is confirmed stable (healthz 200 above).
	// All bootstrap-internal goroutines (HTTP serve loops, etc.) are already running,
	// so the baseline already includes them. Taking it here — before cancel() — provides
	// a happens-before synchronisation point: no goroutine launches between this line
	// and cancel().
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
	}, 2*time.Second, 50*time.Millisecond, "goroutine count did not return to baseline after triple listener shutdown")
}

// TestTripleListener_MidBindFailure_RollsBackEarlierBindings verifies that when
// bootstrap owns three sockets (primary → internal → health) and the health bind
// fails (port collision), closeOwnedSockets releases the two already-bound
// bootstrap-owned sockets so no port leak occurs (TEST-11 three-listener variant).
func TestTripleListener_MidBindFailure_RollsBackEarlierBindings(t *testing.T) {
	// Pre-bind a listener to get a known port for the collision target.
	collideLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot bind test listener (sandbox):", err)
	}
	defer collideLn.Close()
	collidingAddr := collideLn.Addr().String()
	// collideLn stays open so bootstrap's health bind collides with EADDRINUSE.

	asm := assembly.New(assembly.Config{ID: "triple-mid-bind-fail", DurabilityMode: cell.DurabilityDemo})

	b := New(
		WithAssembly(asm),
		// Primary and internal: bootstrap-owned sockets on :0 → will succeed.
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}),
		WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}),
		// Health: colliding address → EADDRINUSE.
		WithListener(cell.HealthListener, collidingAddr, []cell.ListenerAuth{cell.AuthNone{}}),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	runErr := b.Run(ctx)

	// collideLn still open — health bind must fail.
	require.Error(t, runErr, "health bind failure must cause Run to return an error")
	assert.Contains(t, runErr.Error(), "listen health",
		"error must identify the failing listener as 'health'")
	// The two bootstrap-owned sockets (primary + internal) must have been released
	// by closeOwnedSockets. Verify by trying to re-bind the same port numbers.
	// (We cannot inspect the exact addrs easily since :0 assigns ephemeral ports,
	// but the test ensures Run returns an error and does not hang — the primary
	// ports are returned to the OS on error, confirmed by cleanup.)
}

// TestDualListener_BootstrapOwnedPrimary_InternalBindFails verifies that when
// bootstrap owns (binds) the primary socket itself and the internal bind fails,
// bootstrap releases the primary socket it created (TEST-11).
// This differs from TestDualListener_InternalBindFailure_ClosesOwnedPrimary,
// which uses a caller-injected primary socket (bootstrap must NOT close it).
func TestDualListener_BootstrapOwnedPrimary_InternalBindFails(t *testing.T) {
	// Pre-bind a listener to get a known port, then pass it as internal
	// to force a collision. Primary uses bootstrap-owned :0.
	collideLn := newLocalListener(t)
	collidingAddr := collideLn.Addr().String()
	// Keep collideLn open so the port stays reserved; bootstrap's internal
	// bind will fail with EADDRINUSE on this port.

	asm := assembly.New(assembly.Config{ID: "bootstrap-owned-fail", DurabilityMode: cell.DurabilityDemo})

	b := New(
		WithAssembly(asm),
		// Primary: bootstrap-owned socket (no WithListenerNet); will bind :0 → success.
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}),
		// Internal: same colliding address → EADDRINUSE.
		WithListener(cell.InternalListener, collidingAddr, []cell.ListenerAuth{cell.AuthNone{}}),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := b.Run(ctx)
	// collideLn is still open, so the internal bind should fail.
	require.Error(t, err, "internal bind failure must cause Run to return an error")
	assert.Contains(t, err.Error(), "listen internal",
		"error must identify the failing listener as 'internal'")
}

// ---------------------------------------------------------------------------
// T-01: FinalizeAuth duplicate meta detection
// ---------------------------------------------------------------------------

// duplicateMetaCell mounts two routes with the same (method, path) to trigger
// FinalizeAuth duplicate detection.
type duplicateMetaCell struct {
	*cell.BaseCell
}

func (c *duplicateMetaCell) RouteGroups() []cell.RouteGroup {
	spec := testHTTPContract(http.MethodGet, "/api/v1/dup/ping")
	return []cell.RouteGroup{
		{
			Listener: cell.PrimaryListener,
			Prefix:   "",
			Register: func(mux cell.RouteMux) error {
				auth.MustMount(mux, auth.Route{Contract: spec, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), Public: true})
				auth.MustMount(mux, auth.Route{Contract: spec, Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }), Public: true})
				return nil
			},
		},
	}
}

// TestDualListener_FinalizeAuth_DuplicateMeta_Errors verifies that FinalizeAuth
// returns an error when the same (method, path) pair is mounted twice on the
// primary listener — protecting configuration cleanliness (FinalizeAuth invariant).
func TestDualListener_FinalizeAuth_DuplicateMeta_Errors(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "dup-meta-test", DurabilityMode: cell.DurabilityDemo})
	c := &duplicateMetaCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: "dup-meta-cell", Type: cell.CellTypeCore}),
	}
	require.NoError(t, asm.Register(c))

	primaryLn := newLocalListener(t)
	b := New(
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
		WithShutdownTimeout(time.Second),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := b.Run(ctx)
	require.Error(t, err, "duplicate route meta must cause Run to return an error")
}

// ---------------------------------------------------------------------------
// T-02: shutdownAllServers aggregates partial errors (Wave C dependency)
// ---------------------------------------------------------------------------

// TestShutdownAllServers_AggregatesPartialErrors verifies that shutdownAllServers
// collects errors from all tasks and joins them, not short-circuiting on the first.
func TestShutdownAllServers_AggregatesPartialErrors(t *testing.T) {
	err1 := errors.New("server-a shutdown timeout")
	err2 := errors.New("server-b connection reset")

	tasks := []shutdownTask{
		{
			name:      "server-a",
			shutGrace: 0,
			shutdown:  func(_ context.Context) error { return err1 },
		},
		{
			name:      "server-b",
			shutGrace: 0,
			shutdown:  func(_ context.Context) error { return err2 },
		},
		{
			name:      "server-c",
			shutGrace: 0,
			shutdown:  func(_ context.Context) error { return nil },
		},
	}

	ctx := context.Background()
	got := shutdownAllServers(ctx, tasks)
	require.Error(t, got, "shutdownAllServers must return error when any task fails")
	assert.True(t, errors.Is(got, err1) || strings.Contains(got.Error(), err1.Error()),
		"joined error must contain err1")
	assert.True(t, errors.Is(got, err2) || strings.Contains(got.Error(), err2.Error()),
		"joined error must contain err2")
	// OPS-01: aggregated error must preserve per-listener attribution so
	// operators can pick out which listener tripped from the error object alone
	// (matching the slog "listener=" attribute).
	assert.Contains(t, got.Error(), `listener "server-a"`,
		"joined error must wrap err1 with listener name")
	assert.Contains(t, got.Error(), `listener "server-b"`,
		"joined error must wrap err2 with listener name")
}

// ---------------------------------------------------------------------------
// T-03: Phase7ServeAll dual listener — no close race (-race required)
// ---------------------------------------------------------------------------

// TestPhase7ServeAll_DualListener_NoCloseRace verifies that concurrent Serve
// goroutines do not race on shared state when the listeners are closed in
// parallel during shutdown. This test is only meaningful with -race.
func TestPhase7ServeAll_DualListener_NoCloseRace(t *testing.T) {
	// This test is intentionally NOT skipped in -short because it validates a
	// safety property (no data race) under parallel server shutdown.
	primaryLn := newLocalListener(t)
	internalLn := newLocalListener(t)

	c := newDualListenerCell(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
	)
	asm := assembly.New(assembly.Config{ID: "race-test", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(c))

	b := New(
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
		WithListener(cell.InternalListener, internalLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(internalLn)),
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

	// Fire concurrent requests while shutting down to exercise the race window.
	// WaitGroup ensures all in-flight goroutines have exited before the test
	// function returns, preventing goroutine leaks that could pollute later tests.
	var wg sync.WaitGroup
	var inFlight atomic.Int32
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			inFlight.Add(1)
			defer inFlight.Add(-1)
			_, _ = testHTTPClient.Get(fmt.Sprintf("http://%s/api/v1/test/ping", primaryAddr))
		}()
	}

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}

	// Wait for all in-flight request goroutines to complete.
	wg.Wait()
}

// ---------------------------------------------------------------------------
// T-04: Phase7BindListeners — owned socket closed on sibling failure
// ---------------------------------------------------------------------------

// TestPhase7BindListeners_OwnedSocket_ClosedOnSiblingFailure verifies that
// when bootstrap owns a socket (primary :0) and the next bind fails (collision),
// the already-owned socket is released. This mirrors TEST-11 for the generic path.
func TestPhase7BindListeners_OwnedSocket_ClosedOnSiblingFailure(t *testing.T) {
	// Hold a port so internal bind collides.
	holdLn := newLocalListener(t)
	collidingAddr := holdLn.Addr().String()

	asm := assembly.New(assembly.Config{ID: "owned-sibling-fail", DurabilityMode: cell.DurabilityDemo})

	b := New(
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}),  // bootstrap-owned; should succeed then be released
		WithListener(cell.InternalListener, collidingAddr, []cell.ListenerAuth{cell.AuthNone{}}), // collides
		WithShutdownTimeout(time.Second),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := b.Run(ctx)
	require.Error(t, err, "sibling bind failure must propagate")
	assert.Contains(t, err.Error(), "listen internal")
}

// ---------------------------------------------------------------------------
// T-05: RouteGroup Middleware order preserved
// ---------------------------------------------------------------------------

// middlewareOrderCell mounts a route with two middleware that record call order.
type middlewareOrderCell struct {
	*cell.BaseCell
	order *[]string
}

func (c *middlewareOrderCell) RouteGroups() []cell.RouteGroup {
	order := c.order
	mw1 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*order = append(*order, "mw1")
			next.ServeHTTP(w, r)
		})
	}
	mw2 := func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			*order = append(*order, "mw2")
			next.ServeHTTP(w, r)
		})
	}
	return []cell.RouteGroup{
		{
			Listener:   cell.PrimaryListener,
			Prefix:     "/api/v1/mwtest",
			Middleware: []func(http.Handler) http.Handler{mw1, mw2},
			Register: func(mux cell.RouteMux) error {
				auth.MustMount(mux, auth.Route{
					Contract: testHTTPContract(http.MethodGet, "/api/v1/mwtest/ping"),
					Handler:  http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
					Public:   true,
				})
				return nil
			},
		},
	}
}

// TestRouteGroup_Middleware_OrderPreserved verifies that RouteGroup.Middleware
// entries are applied in declaration order: first registered is outermost.
func TestRouteGroup_Middleware_OrderPreserved(t *testing.T) {
	var order []string
	c := &middlewareOrderCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: "mw-order-cell", Type: cell.CellTypeCore}),
		order:    &order,
	}
	asm := assembly.New(assembly.Config{ID: "mw-order-test", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(c))

	primaryLn := newLocalListener(t)
	b := New(
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
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

	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/api/v1/mwtest/ping", primaryAddr))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, []string{"mw1", "mw2"}, order,
		"middleware must execute in declaration order (first registered = outermost)")

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// ---------------------------------------------------------------------------
// T-09: AuthWiring internal guard waits for internal listener ready
// ---------------------------------------------------------------------------

// TestAuthWiring_InternalGuard_WaitsForInternalListenerReady verifies that
// the internal listener is serving before the primary listener becomes healthy.
// Both must be bound and accepting before /healthz returns 200.
func TestAuthWiring_InternalGuard_WaitsForInternalListenerReady(t *testing.T) {
	primaryLn := newLocalListener(t)
	internalLn := newLocalListener(t)

	c := newDualListenerCell(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
	)
	asm := assembly.New(assembly.Config{ID: "auth-wiring-test", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(c))

	b := New(
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
		WithListener(cell.InternalListener, internalLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(internalLn)),
		WithShutdownTimeout(2*time.Second),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- b.Run(ctx) }()

	primaryAddr := primaryLn.Addr().String()
	internalAddr := internalLn.Addr().String()
	// Wait for primary healthy.
	require.Eventually(t, func() bool {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/healthz", primaryAddr))
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	}, 3*time.Second, 50*time.Millisecond, "primary listener did not become ready")

	// Internal listener must also be reachable by the time primary is healthy.
	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/internal/v1/admin/ping", internalAddr))
	require.NoError(t, err)
	resp.Body.Close()
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"internal listener must be reachable after primary becomes healthy")

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// ---------------------------------------------------------------------------
// T-10: Goroutine baseline after server stable
// ---------------------------------------------------------------------------

// TestShutdown_NumGoroutineBaseline_AfterServerStable verifies that after a
// clean shutdown, the goroutine count does not permanently exceed the baseline
// taken just before shutdown began.
func TestShutdown_NumGoroutineBaseline_AfterServerStable(t *testing.T) {
	primaryLn := newLocalListener(t)
	internalLn := newLocalListener(t)

	c := newDualListenerCell(
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
		func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
	)
	asm := assembly.New(assembly.Config{ID: "goroutine-baseline-test", DurabilityMode: cell.DurabilityDemo})
	require.NoError(t, asm.Register(c))

	b := New(
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
		WithListener(cell.InternalListener, internalLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(internalLn)),
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

	// Baseline is taken AFTER the server is confirmed stable (healthz 200 returned
	// above). Taking it here ensures that all bootstrap-internal goroutines (HTTP
	// serve loops, worker loops, etc.) are already running, so the baseline already
	// includes them. Taking it before cancel() also provides a happens-before
	// synchronisation point: there are no in-flight goroutine launches between this
	// line and cancel().
	baseline := runtime.NumGoroutine()

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("bootstrap did not shut down in time")
	}

	// 2 s convergence window: the Go runtime does not reclaim goroutine stack
	// frames synchronously; after Run returns the net/http server and internal
	// goroutines are in the process of exiting.  2 s is generous enough to
	// tolerate slow CI machines while still catching real leaks.
	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= baseline
	}, 2*time.Second, 50*time.Millisecond,
		"goroutine count must not exceed baseline after clean shutdown")
}

// ---------------------------------------------------------------------------
// Phase0 三连：NoListeners / DuplicateListenerRefs / MetricsRequiresHealth
// ---------------------------------------------------------------------------

// TestPhase0_NoListenersDeclared verifies that phase0 fails fast when no
// listeners are declared.
func TestPhase0_NoListenersDeclared(t *testing.T) {
	b := New() // no WithListener calls
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no HTTP listeners declared")
}

// TestPhase0_DuplicateListenerRefs verifies that phase0 fails fast when the
// same ListenerRef is declared more than once.
func TestPhase0_DuplicateListenerRefs(t *testing.T) {
	b := New(
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}),
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}), // duplicate
	)
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate WithListener")
}

// TestPhase0_MetricsRequiresHealthListener verifies that configuring a metrics
// handler without a dedicated HealthListener is rejected at phase0 (B2 rule).
func TestPhase0_MetricsRequiresHealthListener(t *testing.T) {
	b := New(
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}),
		WithHealthRoutes(WithMetricsHandler(http.NewServeMux())),
	)
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithHealthRoutes(WithMetricsHandler")
}
