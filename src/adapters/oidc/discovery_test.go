package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockOIDCServer returns a test server that mimics OIDC discovery endpoints.
func mockOIDCServer(t *testing.T) *httptest.Server {
	t.Helper()

	// Generate a temporary RSA key for JWKS.
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	_ = key // We need it for JWKS but go-oidc only requires the metadata and a reachable JWKS endpoint.

	mux := http.NewServeMux()
	var issuer string

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		discovery := map[string]any{
			"issuer":                 issuer,
			"authorization_endpoint": issuer + "/auth",
			"token_endpoint":         issuer + "/token",
			"userinfo_endpoint":      issuer + "/userinfo",
			"jwks_uri":               issuer + "/jwks",
			"response_types_supported": []string{"code"},
			"subject_types_supported":  []string{"public"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(discovery)
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		jwks := map[string]any{
			"keys": []any{},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jwks)
	})

	srv := httptest.NewServer(mux)
	issuer = srv.URL
	return srv
}

func TestProvider_Discovery(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	adapter, err := New(Config{IssuerURL: srv.URL, ClientID: "test-client"})
	require.NoError(t, err)

	p, err := adapter.Provider(context.Background())
	require.NoError(t, err)
	require.NotNil(t, p)
}

func TestProvider_Cache(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	adapter, err := New(Config{IssuerURL: srv.URL, ClientID: "test-client"})
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

	adapter, err := New(Config{IssuerURL: srv.URL, ClientID: "test-client"})
	require.NoError(t, err)

	// Initial discovery.
	_, err = adapter.Provider(context.Background())
	require.NoError(t, err)

	// Refresh should re-discover.
	p, err := adapter.Refresh(context.Background())
	require.NoError(t, err)
	require.NotNil(t, p)
}

func TestVerifier(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	adapter, err := New(Config{IssuerURL: srv.URL, ClientID: "test-client"})
	require.NoError(t, err)

	v, err := adapter.Verifier(context.Background())
	require.NoError(t, err)
	require.NotNil(t, v)
}

func TestOAuth2Config_DefaultScopes(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	adapter, err := New(Config{IssuerURL: srv.URL, ClientID: "test-client", ClientSecret: "secret", RedirectURL: "http://localhost/callback"})
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

	adapter, err := New(Config{
		IssuerURL: srv.URL, ClientID: "test-client",
		Scopes: []string{"openid", "custom"},
	})
	require.NoError(t, err)

	cfg, err := adapter.OAuth2Config(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []string{"openid", "custom"}, cfg.Scopes)
}

func TestProvider_DiscoveryError(t *testing.T) {
	adapter, err := New(Config{IssuerURL: "http://127.0.0.1:1", ClientID: "test-client"})
	require.NoError(t, err)

	_, err = adapter.Provider(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "discovery failed")
}
