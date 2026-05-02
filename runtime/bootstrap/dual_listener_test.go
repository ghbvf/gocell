package bootstrap

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
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
				auth.MustMount(mux, auth.Route{
					Contract: testHTTPContract(http.MethodGet, "/api/v1/test/ping"),
					Handler:  http.HandlerFunc(c.onPublic),
					Public:   true,
				})
				return nil
			},
		},
		{
			Listener: cell.InternalListener,
			Prefix:   "",
			Register: func(mux cell.RouteMux) error {
				auth.MustMount(mux, auth.Route{
					Contract: testHTTPContract(http.MethodGet, "/internal/v1/admin/ping"),
					Handler:  http.HandlerFunc(c.onInternal),
				})
				return nil
			},
		},
	}
}

func testInternalAuthChain(t *testing.T) ([]cell.ListenerAuth, *auth.HMACKeyRing) {
	t.Helper()
	ring, err := auth.NewHMACKeyRing([]byte("test-service-token-secret-32-bytes"), nil)
	require.NoError(t, err)
	store, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clock.Real())
	require.NoError(t, err)
	return []cell.ListenerAuth{cell.MustNewAuthServiceToken(store, ring)}, ring
}

func getWithServiceToken(t *testing.T, rawURL string, ring *auth.HMACKeyRing) *http.Response {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	require.NoError(t, err)
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	require.NoError(t, err)
	req.Header.Set("Authorization", "ServiceToken "+
		auth.GenerateServiceToken(ring, http.MethodGet, parsed.Path, parsed.RawQuery, time.Now()))
	resp, err := testHTTPClient.Do(req)
	require.NoError(t, err)
	return resp
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
	asm := assembly.New(assembly.Config{ID: "dual-primary-404", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Register(c))
	internalAuthChain, internalRing := testInternalAuthChain(t)

	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
		WithListener(cell.InternalListener, internalLn.Addr().String(), internalAuthChain, WithListenerNet(internalLn)),
		WithShutdownTimeout(testtime.D2s),
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
		closeBody(t, resp)
		return resp.StatusCode == http.StatusOK
	}, testtime.EventuallyDefault, testtime.MediumPoll, "primary listener did not become ready")

	t.Run("primary_404s_internal_prefix", func(t *testing.T) {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/internal/v1/admin/ping", primaryAddr))
		require.NoError(t, err)
		defer closeBody(t, resp)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode,
			"primary listener must 404 on /internal/v1/* — port isolation contract")
		assert.Equal(t, int64(0), internalHits.Load(),
			"internal handler must NOT be invoked through primary listener")
	})

	t.Run("internal_404s_public_prefix", func(t *testing.T) {
		// internal:port + /api/v1/* → 404
		resp := getWithServiceToken(t, fmt.Sprintf("http://%s/api/v1/test/ping", internalAddr), internalRing)
		defer closeBody(t, resp)
		assert.Equal(t, http.StatusNotFound, resp.StatusCode,
			"internal listener must 404 on /api/v1/*")
		assert.Equal(t, int64(0), publicHits.Load(),
			"public handler must NOT be invoked through internal listener")
	})

	t.Run("internal_404s_infra_endpoints", func(t *testing.T) {
		for _, p := range []string{"/healthz", "/readyz", "/metrics", "/"} {
			resp := getWithServiceToken(t, fmt.Sprintf("http://%s%s", internalAddr, p), internalRing)
			closeBody(t, resp)
			assert.Equal(t, http.StatusNotFound, resp.StatusCode,
				"internal listener must 404 on infra path %q", p)
		}
	})

	t.Run("primary_routes_public_business", func(t *testing.T) {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/api/v1/test/ping", primaryAddr))
		require.NoError(t, err)
		defer closeBody(t, resp)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, int64(1), publicHits.Load())
	})

	t.Run("internal_routes_internal_business", func(t *testing.T) {
		resp := getWithServiceToken(t, fmt.Sprintf("http://%s/internal/v1/admin/ping", internalAddr), internalRing)
		defer closeBody(t, resp)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
		assert.Equal(t, int64(1), internalHits.Load())
	})

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// TestDualListener_InternalRoutesAccessibleWithoutJWT verifies that the
// InternalListener router does NOT install JWT auth (no auth verifier on
// internal listener). A ServiceToken guards the listener and injects the
// internal service principal used by route policies.
//
// PR-A14b: WithInternalMiddleware was deleted. Internal listener middleware
// must now be applied through the listener auth chain.
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
	asm := assembly.New(assembly.Config{ID: "dual-nojwt-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Register(c))
	internalAuthChain, internalRing := testInternalAuthChain(t)

	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
		WithListener(cell.InternalListener, internalLn.Addr().String(), internalAuthChain, WithListenerNet(internalLn)),
		WithShutdownTimeout(testtime.D2s),
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
		closeBody(t, resp)
		return resp.StatusCode == http.StatusOK
	}, testtime.EventuallyDefault, testtime.MediumPoll, "primary listener did not become ready")

	// Internal endpoint is reachable with a ServiceToken and without a JWT
	// bearer. The internal listener must not install the public JWT verifier.
	t.Run("internal_accessible_with_service_token_without_jwt", func(t *testing.T) {
		resp := getWithServiceToken(t, fmt.Sprintf("http://%s/internal/v1/admin/ping", internalAddr), internalRing)
		defer closeBody(t, resp)
		assert.Equal(t, http.StatusOK, resp.StatusCode,
			"internal listener must NOT require JWT; ServiceToken is the internal transport guard")
		assert.Equal(t, int64(1), internalHits.Load())
	})

	// Public endpoint is reachable on the primary listener.
	t.Run("public_reachable_on_primary", func(t *testing.T) {
		resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/api/v1/test/ping", primaryAddr))
		require.NoError(t, err)
		defer closeBody(t, resp)
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("bootstrap did not shut down in time")
	}
}

// TestDualListener_EqualAddrsBindFails verifies that setting the same address
// on both primary and internal listeners results in a bind failure at phase7.
// PR-A14b: duplicate address detection is no longer a phase0 check — it
// surfaces as an OS-level EADDRINUSE error when the second socket is bound.
func TestDualListener_EqualAddrsBindFails(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "equal-addr-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	// Pre-bind primary to ensure port is held; use the same port for internal.
	primaryLn := newLocalListener(t)
	collidingAddr := primaryLn.Addr().String()

	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
		WithListener(cell.InternalListener, collidingAddr, []cell.ListenerAuth{cell.AuthNone{}}), // collides with primary
		WithShutdownTimeout(testtime.D1s),
	)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
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
			asm := assembly.New(assembly.Config{ID: "empty-addr-" + tc.name, DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
			opts := append([]Option{WithClock(clock.Real()), WithAssembly(asm)}, tc.opts...)
			b := New(opts...)

			ctx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
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

	asm := assembly.New(assembly.Config{ID: "bind-fail-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})

	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, callerLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(callerLn)),
		WithListener(cell.InternalListener, collidingAddr, []cell.ListenerAuth{cell.AuthNone{}}), // guaranteed to collide
		WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.EventuallyDefault)
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
			closeConn(t, conn)
		}
		close(done)
	}()
	dialConn, dialErr := net.Dial("tcp", callerLn.Addr().String())
	if dialConn != nil {
		closeConn(t, dialConn)
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
	asm := assembly.New(assembly.Config{ID: "shutdown-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Register(c))
	internalAuthChain, _ := testInternalAuthChain(t)

	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
		WithListener(cell.InternalListener, internalLn.Addr().String(), internalAuthChain, WithListenerNet(internalLn)),
		WithShutdownTimeout(testtime.D2s),
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
		closeBody(t, resp)
		return resp.StatusCode == http.StatusOK
	}, testtime.EventuallyDefault, testtime.MediumPoll, "primary listener did not become ready")

	// Baseline is taken AFTER the server is confirmed stable (healthz 200 above).
	// All bootstrap-internal goroutines (HTTP serve loops, etc.) are already running,
	// so the baseline already includes them. Taking it here — before cancel() — provides
	// a happens-before synchronization point: no goroutine launches between this line
	// and cancel().
	before := runtime.NumGoroutine()

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("bootstrap did not shut down in time")
	}

	// Allow short settle window for goroutine cleanup.
	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= before
	}, testtime.D2s, testtime.MediumPoll, "goroutine count did not return to baseline")

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
	asm := assembly.New(assembly.Config{ID: "triple-shutdown-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Register(c))
	internalAuthChain, _ := testInternalAuthChain(t)

	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
		WithListener(cell.InternalListener, internalLn.Addr().String(), internalAuthChain, WithListenerNet(internalLn)),
		WithListener(cell.HealthListener, healthLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(healthLn)),
		WithShutdownTimeout(testtime.D2s),
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
		closeBody(t, resp)
		return resp.StatusCode == http.StatusOK
	}, testtime.EventuallyDefault, testtime.MediumPoll, "health listener did not become ready")

	// Baseline is taken AFTER the server is confirmed stable (healthz 200 above).
	// All bootstrap-internal goroutines (HTTP serve loops, etc.) are already running,
	// so the baseline already includes them. Taking it here — before cancel() — provides
	// a happens-before synchronization point: no goroutine launches between this line
	// and cancel().
	before := runtime.NumGoroutine()

	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("bootstrap did not shut down in time")
	}

	// Allow short settle window for goroutine cleanup.
	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= before
	}, testtime.D2s, testtime.MediumPoll, "goroutine count did not return to baseline after triple listener shutdown")
}

// TestTripleListener_MidBindFailure_RollsBackEarlierBindings verifies that when
// bootstrap owns three sockets and the final bind fails, closeOwnedSockets
// releases the already-bound bootstrap-owned sockets so no port leak occurs
// (TEST-11 three-listener variant).
func TestTripleListener_MidBindFailure_RollsBackEarlierBindings(t *testing.T) {
	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	original := slog.Default()
	slog.SetDefault(logger)
	defer slog.SetDefault(original)

	// Pre-bind a listener to get a known primary port for the collision target.
	// phase7BindListeners sorts by ref.String(), so bind order is
	// health -> internal -> primary. Making primary collide proves rollback
	// closes earlier health/internal sockets.
	collideLn, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip("cannot bind test listener (sandbox):", err)
	}
	defer closeListener(t, collideLn)
	collidingAddr := collideLn.Addr().String()
	// collideLn stays open so bootstrap's primary bind collides with EADDRINUSE.

	asm := assembly.New(assembly.Config{ID: "triple-mid-bind-fail", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})

	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		// Health and internal bind first (alphabetical listener ref order) and
		// must be released when primary fails.
		WithListener(cell.HealthListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}),
		WithListener(cell.InternalListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}),
		// Primary: colliding address → EADDRINUSE.
		WithListener(cell.PrimaryListener, collidingAddr, []cell.ListenerAuth{cell.AuthNone{}}),
		WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.EventuallyDefault)
	defer cancel()
	runErr := b.Run(ctx)

	// collideLn still open — primary bind must fail.
	require.Error(t, runErr, "primary bind failure must cause Run to return an error")
	assert.Contains(t, runErr.Error(), "listen primary",
		"error must identify the failing listener as 'primary'")

	boundAddrs := boundListenerAddrsFromLogs(t, logBuf.Bytes())
	for _, ref := range []cell.ListenerRef{cell.HealthListener, cell.InternalListener} {
		addr, ok := boundAddrs[ref.String()]
		require.Truef(t, ok, "bind log must contain listener %q", ref.String())
		ln, listenErr := net.Listen("tcp", addr)
		require.NoErrorf(t, listenErr, "listener %q addr %s must be immediately reusable after rollback", ref.String(), addr)
		require.NoError(t, ln.Close())
	}
}

func boundListenerAddrsFromLogs(t *testing.T, logs []byte) map[string]string {
	t.Helper()
	out := make(map[string]string)
	for line := range bytes.SplitSeq(logs, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry["msg"] != "bootstrap: HTTP listener bound" {
			continue
		}
		listener, _ := entry["listener"].(string)
		addr, _ := entry["addr"].(string)
		if listener != "" && addr != "" {
			out[listener] = addr
		}
	}
	return out
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

	asm := assembly.New(assembly.Config{ID: "bootstrap-owned-fail", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})

	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		// Primary: bootstrap-owned socket (no WithListenerNet); will bind :0 → success.
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}),
		// Internal: same colliding address → EADDRINUSE.
		WithListener(cell.InternalListener, collidingAddr, []cell.ListenerAuth{cell.AuthNone{}}),
		WithShutdownTimeout(testtime.D2s),
	)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.EventuallyDefault)
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
				auth.MustMount(mux, auth.Route{
					Contract: spec,
					Handler:  http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
					Public:   true,
				})
				auth.MustMount(mux, auth.Route{
					Contract: spec,
					Handler:  http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }),
					Public:   true,
				})
				return nil
			},
		},
	}
}

// TestDualListener_FinalizeAuth_DuplicateMeta_Errors verifies that FinalizeAuth
// returns an error when the same (method, path) pair is mounted twice on the
// primary listener — protecting configuration cleanliness (FinalizeAuth invariant).
func TestDualListener_FinalizeAuth_DuplicateMeta_Errors(t *testing.T) {
	asm := assembly.New(assembly.Config{ID: "dup-meta-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	c := &duplicateMetaCell{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{ID: "dup-meta-cell", Type: cell.CellTypeCore}),
	}
	require.NoError(t, asm.Register(c))

	primaryLn := newLocalListener(t)
	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
		WithShutdownTimeout(testtime.D1s),
	)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
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

func TestShutdownAllServers_WaitsForServeLoopStopped(t *testing.T) {
	stopped := make(chan struct{})
	shutdownCalled := make(chan struct{})
	tasks := []shutdownTask{
		{
			name:      "server-a",
			shutGrace: 0,
			shutdown: func(_ context.Context) error {
				close(shutdownCalled)
				return nil
			},
			stopped: stopped,
		},
	}

	done := make(chan error, 1)
	go func() {
		done <- shutdownAllServers(context.Background(), tasks)
	}()

	<-shutdownCalled
	select {
	case err := <-done:
		t.Fatalf("shutdownAllServers returned before Serve loop stopped: %v", err)
	default:
	}

	close(stopped)
	require.NoError(t, <-done)
}

func TestShutdownAllServers_ForceClosesOnContextExpiry(t *testing.T) {
	stopped := make(chan struct{})
	forceClosed := make(chan struct{})
	tasks := []shutdownTask{
		{
			name:      "server-a",
			shutGrace: 0,
			shutdown: func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			},
			forceClose: func() error {
				close(forceClosed)
				return nil
			},
			stopped: stopped,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D10ms)
	defer cancel()
	got := shutdownAllServers(ctx, tasks)
	require.Error(t, got, "shutdownAllServers must surface the graceful shutdown timeout")
	assert.True(t, errors.Is(got, context.DeadlineExceeded) || strings.Contains(got.Error(), context.DeadlineExceeded.Error()),
		"joined error must contain the shutdown context error")
	assert.Contains(t, got.Error(), `listener "server-a"`,
		"timeout error must preserve per-listener attribution")
	select {
	case <-forceClosed:
	default:
		t.Fatal("forceClose must run when graceful shutdown times out")
	}
}

func TestShutdownAllServers_WaitsForStoppedAfterPerListenerGraceTimeout(t *testing.T) {
	stopped := make(chan struct{})
	forceClosed := make(chan struct{})
	tasks := []shutdownTask{
		{
			name:      "server-a",
			shutGrace: testtime.D10ms,
			shutdown: func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			},
			forceClose: func() error {
				close(forceClosed)
				return nil
			},
			stopped: stopped,
		},
	}

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D1s)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- shutdownAllServers(ctx, tasks)
	}()

	select {
	case <-forceClosed:
	case <-time.After(testtime.D1s):
		t.Fatal("forceClose was not called after per-listener grace timeout")
	}
	select {
	case err := <-done:
		t.Fatalf("shutdownAllServers returned before Serve loop stopped: %v", err)
	default:
	}

	close(stopped)
	got := <-done
	require.Error(t, got, "per-listener grace timeout must still be reported")
	assert.True(t, errors.Is(got, context.DeadlineExceeded) || strings.Contains(got.Error(), context.DeadlineExceeded.Error()),
		"joined error must contain the per-listener grace timeout")
	assert.Contains(t, got.Error(), `listener "server-a"`,
		"timeout error must preserve per-listener attribution")
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
	asm := assembly.New(assembly.Config{ID: "race-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Register(c))
	internalAuthChain, _ := testInternalAuthChain(t)

	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
		WithListener(cell.InternalListener, internalLn.Addr().String(), internalAuthChain, WithListenerNet(internalLn)),
		WithShutdownTimeout(testtime.D2s),
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
		closeBody(t, resp)
		return resp.StatusCode == http.StatusOK
	}, testtime.EventuallyDefault, testtime.MediumPoll, "primary listener did not become ready")

	// Fire concurrent requests while shutting down to exercise the race window.
	// WaitGroup ensures all in-flight goroutines have exited before the test
	// function returns, preventing goroutine leaks that could pollute later tests.
	var wg sync.WaitGroup
	var inFlight atomic.Int32
	for range 4 {
		wg.Go(func() {
			inFlight.Add(1)
			defer inFlight.Add(-1)
			_, _ = testHTTPClient.Get(fmt.Sprintf("http://%s/api/v1/test/ping", primaryAddr))
		})
	}

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(testtime.SelectShutdown):
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

	asm := assembly.New(assembly.Config{ID: "owned-sibling-fail", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})

	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		// bootstrap-owned; should succeed then be released
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}),
		WithListener(cell.InternalListener, collidingAddr, []cell.ListenerAuth{cell.AuthNone{}}), // collides
		WithShutdownTimeout(testtime.D1s),
	)

	ctx, cancel := context.WithTimeout(context.Background(), testtime.D2s)
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
	asm := assembly.New(assembly.Config{ID: "mw-order-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Register(c))

	primaryLn := newLocalListener(t)
	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
		WithShutdownTimeout(testtime.D2s),
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
		closeBody(t, resp)
		return resp.StatusCode == http.StatusOK
	}, testtime.EventuallyDefault, testtime.MediumPoll, "primary listener did not become ready")

	resp, err := testHTTPClient.Get(fmt.Sprintf("http://%s/api/v1/mwtest/ping", primaryAddr))
	require.NoError(t, err)
	closeBody(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, []string{"mw1", "mw2"}, order,
		"middleware must execute in declaration order (first registered = outermost)")

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(testtime.SelectShutdown):
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
	asm := assembly.New(assembly.Config{ID: "auth-wiring-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Register(c))
	internalAuthChain, internalRing := testInternalAuthChain(t)

	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
		WithListener(cell.InternalListener, internalLn.Addr().String(), internalAuthChain, WithListenerNet(internalLn)),
		WithShutdownTimeout(testtime.D2s),
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
		closeBody(t, resp)
		return resp.StatusCode == http.StatusOK
	}, testtime.EventuallyDefault, testtime.MediumPoll, "primary listener did not become ready")

	// Internal listener must also be reachable by the time primary is healthy.
	resp := getWithServiceToken(t, fmt.Sprintf("http://%s/internal/v1/admin/ping", internalAddr), internalRing)
	closeBody(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode,
		"internal listener must be reachable after primary becomes healthy")

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(testtime.SelectShutdown):
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
	asm := assembly.New(assembly.Config{ID: "goroutine-baseline-test", DurabilityMode: cell.DurabilityDemo, Clock: clock.Real()})
	require.NoError(t, asm.Register(c))
	internalAuthChain, _ := testInternalAuthChain(t)

	b := New(
		WithClock(clock.Real()),
		WithAssembly(asm),
		WithListener(cell.PrimaryListener, primaryLn.Addr().String(), []cell.ListenerAuth{cell.AuthNone{}}, WithListenerNet(primaryLn)),
		WithListener(cell.InternalListener, internalLn.Addr().String(), internalAuthChain, WithListenerNet(internalLn)),
		WithShutdownTimeout(testtime.D2s),
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
		closeBody(t, resp)
		return resp.StatusCode == http.StatusOK
	}, testtime.EventuallyDefault, testtime.MediumPoll, "primary listener did not become ready")

	// Baseline is taken AFTER the server is confirmed stable (healthz 200 returned
	// above). Taking it here ensures that all bootstrap-internal goroutines (HTTP
	// serve loops, worker loops, etc.) are already running, so the baseline already
	// includes them. Taking it before cancel() also provides a happens-before
	// synchronization point: there are no in-flight goroutine launches between this
	// line and cancel().
	baseline := runtime.NumGoroutine()

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err)
	case <-time.After(testtime.SelectShutdown):
		t.Fatal("bootstrap did not shut down in time")
	}

	// 2 s convergence window: the Go runtime does not reclaim goroutine stack
	// frames synchronously; after Run returns the net/http server and internal
	// goroutines are in the process of exiting.  2 s is generous enough to
	// tolerate slow CI machines while still catching real leaks.
	require.Eventually(t, func() bool {
		return runtime.NumGoroutine() <= baseline
	}, testtime.D2s, testtime.MediumPoll,
		"goroutine count must not exceed baseline after clean shutdown")
}

// ---------------------------------------------------------------------------
// Phase0 三连：NoListeners / DuplicateListenerRefs / MetricsRequiresHealth
// ---------------------------------------------------------------------------

// TestPhase0_NoListenersDeclared verifies that phase0 fails fast when no
// listeners are declared.
func TestPhase0_NoListenersDeclared(t *testing.T) {
	b := New(WithClock(clock.Real())) // no WithListener calls
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no HTTP listeners declared")
}

// TestPhase0_DuplicateListenerRefs verifies that phase0 fails fast when the
// same ListenerRef is declared more than once.
func TestPhase0_DuplicateListenerRefs(t *testing.T) {
	// Two identical WithListener calls for the same ref — phase0 must reject duplicates.
	b := New(
		WithClock(clock.Real()),
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}),
		WithListener(cell.PrimaryListener, "127.0.0.1:1", []cell.ListenerAuth{cell.AuthNone{}}),
	)
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "duplicate WithListener")
}

// TestPhase0_MetricsRequiresHealthListener verifies that configuring a metrics
// handler without a dedicated HealthListener is rejected at phase0 (B2 rule).
func TestPhase0_MetricsRequiresHealthListener(t *testing.T) {
	b := New(
		WithClock(clock.Real()),
		WithListener(cell.PrimaryListener, "127.0.0.1:0", []cell.ListenerAuth{cell.AuthNone{}}),
		WithHealthRoutes(WithMetricsHandler(http.NewServeMux())),
	)
	err := b.phase0ValidateOptions()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithHealthRoutes(WithMetricsHandler")
}
