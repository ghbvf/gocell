package oidc

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

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

func TestProvider_DiscoveryError(t *testing.T) {
	// After the breaking constructor change, New itself fails for unreachable
	// issuers (fail-fast at boot). The error carries the discovery failure.
	_, err := New(context.Background(), Config{IssuerURL: "http://127.0.0.1:1", ClientID: "test-client", Clock: clockmock.New(testEpoch)})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "discovery failed")
}
