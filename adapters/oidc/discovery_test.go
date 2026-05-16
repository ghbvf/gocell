package oidc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock/clockmock"
)

// mockOIDCServer returns a test server that mimics OIDC discovery endpoints.
func mockOIDCServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv, _ := mockOIDCServerTogglable(t)
	return srv
}

// mockOIDCServerTogglable returns a test server plus an atomic flag that can
// be set to 1 to make the discovery endpoint return 503 (simulates IdP
// unreachable mid-test for fail-open test cases).
func mockOIDCServerTogglable(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()

	var failDiscovery atomic.Int32 // 0 = healthy, 1 = fail
	mux := http.NewServeMux()
	var issuer string

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if failDiscovery.Load() != 0 {
			http.Error(w, "simulated IdP unreachable", http.StatusServiceUnavailable)
			return
		}
		discovery := map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/auth",
			"token_endpoint":                        issuer + "/token",
			"userinfo_endpoint":                     issuer + "/userinfo",
			"jwks_uri":                              issuer + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(discovery); err != nil {
			t.Logf("encode discovery: %v", err)
		}
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		jwks := map[string]any{
			"keys": []any{},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(jwks); err != nil {
			t.Logf("encode jwks: %v", err)
		}
	})

	srv := httptest.NewServer(mux)
	issuer = srv.URL
	return srv, &failDiscovery
}

func TestProvider_Discovery(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	adapter, err := New(context.Background(), Config{IssuerURL: srv.URL, ClientID: "test-client", Clock: clockmock.New(testEpoch)})
	require.NoError(t, err)

	p, err := adapter.Provider(context.Background())
	require.NoError(t, err)
	require.NotNil(t, p)
}

func TestProvider_Cache(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	adapter, err := New(context.Background(), Config{IssuerURL: srv.URL, ClientID: "test-client", Clock: clockmock.New(testEpoch)})
	require.NoError(t, err)

	// First call — discovery.
	p1, err := adapter.Provider(context.Background())
	require.NoError(t, err)

	// Second call — cached, no network.
	p2, err := adapter.Provider(context.Background())
	require.NoError(t, err)

	assert.Same(t, p1, p2, "second call should return cached provider")
}

func TestRefresh(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	adapter, err := New(context.Background(), Config{IssuerURL: srv.URL, ClientID: "test-client", Clock: clockmock.New(testEpoch)})
	require.NoError(t, err)

	// Refresh should re-discover (provider already cached from New).
	p, err := adapter.Refresh(context.Background())
	require.NoError(t, err)
	require.NotNil(t, p)
}

func TestVerifier(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	adapter, err := New(context.Background(), Config{IssuerURL: srv.URL, ClientID: "test-client", Clock: clockmock.New(testEpoch)})
	require.NoError(t, err)

	v, err := adapter.Verifier(context.Background())
	require.NoError(t, err)
	require.NotNil(t, v)
}

func TestOAuth2Config_DefaultScopes(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	adapter, err := New(context.Background(), Config{
		IssuerURL: srv.URL, ClientID: "test-client",
		ClientSecret: "secret", RedirectURL: "http://localhost/callback",
		Clock: clockmock.New(testEpoch),
	})
	require.NoError(t, err)

	cfg, err := adapter.OAuth2Config(context.Background())
	require.NoError(t, err)

	assert.Equal(t, "test-client", cfg.ClientID)
	assert.Equal(t, "secret", cfg.ClientSecret)
	assert.Equal(t, "http://localhost/callback", cfg.RedirectURL)
	assert.Contains(t, cfg.Scopes, "openid")
	assert.Contains(t, cfg.Scopes, "profile")
	assert.Contains(t, cfg.Scopes, "email")
}

func TestOAuth2Config_CustomScopes(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	adapter, err := New(context.Background(), Config{
		IssuerURL: srv.URL, ClientID: "test-client",
		Scopes: []string{"openid", "custom"},
		Clock:  clockmock.New(testEpoch),
	})
	require.NoError(t, err)

	cfg, err := adapter.OAuth2Config(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{"openid", "custom"}, cfg.Scopes)
}

// TestProvider_ConcurrentReadDuringSlowRefresh is the permanent regression
// guard for refresh_worker.go invariant (a) "availability": a slow or hung
// re-discovery must NOT block concurrent readers. Before the PR #504 review
// fix, discover() held a write lock across gooidc.NewProvider, so a stuck
// refresh blocked Provider()/Verifier()/readyz for up to HTTPTimeout. With
// the atomic.Pointer design the network round-trip holds no lock, so readers
// keep serving the old provider with no measurable delay.
//
// A future edit that re-introduces a lock around the discovery network call
// makes this test hang past the 2s bound and fail.
func TestProvider_ConcurrentReadDuringSlowRefresh(t *testing.T) {
	var block atomic.Bool
	var inflight atomic.Int32
	released := make(chan struct{})

	var issuer string
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		if block.Load() {
			inflight.Add(1)
			select {
			case <-released:
			case <-r.Context().Done():
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"issuer":                                issuer,
			"authorization_endpoint":                issuer + "/auth",
			"token_endpoint":                        issuer + "/token",
			"jwks_uri":                              issuer + "/jwks",
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{}})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	issuer = srv.URL

	// New() discovers synchronously while the handler is unblocked.
	a, err := New(context.Background(), Config{IssuerURL: srv.URL, ClientID: "test-client", Clock: clockmock.New(testEpoch)})
	require.NoError(t, err)
	oldProvider, err := a.Provider(context.Background())
	require.NoError(t, err)
	require.NotNil(t, oldProvider)

	// From now on the discovery endpoint hangs until released.
	block.Store(true)
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(released) }) }
	defer release() // always unblock so the server handler/goroutine exits.

	refreshCtx, cancelRefresh := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancelRefresh()
	refreshDone := make(chan struct{})
	go func() {
		defer close(refreshDone)
		_, _ = a.Refresh(refreshCtx) // blocks inside gooidc.NewProvider
	}()

	// Wait until the refresh is parked inside the network round-trip — this is
	// exactly the window where the old lock-holding implementation blocked all
	// readers.
	require.Eventually(t, func() bool {
		return inflight.Load() >= 1
	}, 5*time.Second, 5*time.Millisecond, "refresh must reach the blocked discovery call")

	// 50 concurrent readers must each return the old provider immediately.
	const readers = 50
	var wg sync.WaitGroup
	wg.Add(readers)
	start := time.Now()
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			p, perr := a.Provider(context.Background())
			assert.NoError(t, perr)
			assert.Same(t, oldProvider, p, "fail-open availability: readers must keep the old provider while refresh is stuck")
		}()
	}
	wg.Wait()
	require.Less(t, time.Since(start), 2*time.Second,
		"reads must not block on the stuck refresh (lock-free atomic.Pointer)")

	release()
	cancelRefresh()
	<-refreshDone
}

func TestProvider_DiscoveryError(t *testing.T) {
	// After the breaking constructor change, New itself fails for unreachable
	// issuers (fail-fast at boot). The error carries the discovery failure.
	_, err := New(context.Background(), Config{IssuerURL: "http://127.0.0.1:1", ClientID: "test-client", Clock: clockmock.New(testEpoch)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "discovery failed")
}
