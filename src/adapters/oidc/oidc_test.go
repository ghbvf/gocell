package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// jwkJSON is the standard JSON Web Key format for test JWKS endpoints.
type jwkJSON struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Use string `json:"use"`
	Alg string `json:"alg"`
	N   string `json:"n"`
	E   string `json:"e"`
}

// jwksJSON is the standard JWKS format.
type jwksJSON struct {
	Keys []jwkJSON `json:"keys"`
}

// testOIDCServer creates an httptest.Server that simulates an OIDC provider.
// It serves discovery, JWKS, token, and userinfo endpoints in standard format
// compatible with coreos/go-oidc.
func testOIDCServer(t *testing.T, privateKey *rsa.PrivateKey, kid string) *httptest.Server {
	t.Helper()

	mux := http.NewServeMux()
	var serverURL string

	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		doc := map[string]any{
			"issuer":                                serverURL,
			"authorization_endpoint":                serverURL + "/authorize",
			"token_endpoint":                        serverURL + "/token",
			"userinfo_endpoint":                     serverURL + "/userinfo",
			"jwks_uri":                              serverURL + "/jwks",
			"scopes_supported":                      []string{"openid", "profile", "email"},
			"id_token_signing_alg_values_supported": []string{"RS256"},
			"response_types_supported":              []string{"code"},
			"subject_types_supported":               []string{"public"},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(doc); err != nil {
			t.Errorf("failed to encode discovery doc: %v", err)
		}
	})

	mux.HandleFunc("/jwks", func(w http.ResponseWriter, r *http.Request) {
		pubKey := &privateKey.PublicKey
		jwks := jwksJSON{
			Keys: []jwkJSON{
				{
					Kty: "RSA",
					Kid: kid,
					Use: "sig",
					Alg: "RS256",
					N:   base64.RawURLEncoding.EncodeToString(pubKey.N.Bytes()),
					E:   base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pubKey.E)).Bytes()),
				},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(jwks); err != nil {
			t.Errorf("failed to encode JWKS: %v", err)
		}
	})

	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		// Build a real JWT for the id_token so tests can verify it.
		now := time.Now()
		claims := jwt.MapClaims{
			"iss": serverURL,
			"sub": "exchange-user",
			"aud": "test-client",
			"exp": now.Add(1 * time.Hour).Unix(),
			"iat": now.Unix(),
		}
		idToken := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
		idToken.Header["kid"] = kid
		rawIDToken, _ := idToken.SignedString(privateKey)

		resp := map[string]any{
			"access_token": "test-access-token",
			"token_type":   "Bearer",
			"expires_in":   3600,
			"id_token":     rawIDToken,
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("failed to encode token response: %v", err)
		}
	})

	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer test-access-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		info := map[string]any{
			"sub":            "user-123",
			"name":           "Test User",
			"email":          "test@example.com",
			"email_verified": true,
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(info); err != nil {
			t.Errorf("failed to encode userinfo: %v", err)
		}
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skipf("skipping: cannot listen on TCP (sandbox?): %v", err)
		return nil
	}
	ln.Close()

	server := httptest.NewServer(mux)
	serverURL = server.URL
	return server
}

func generateTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key
}

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  Config
		wantErr bool
		errCode errcode.Code
	}{
		{
			name:    "valid config",
			config:  DefaultConfig("https://example.com", "client-id", "secret"),
			wantErr: false,
		},
		{
			name:    "missing issuer",
			config:  Config{ClientID: "id"},
			wantErr: true,
			errCode: ErrAdapterOIDCConfig,
		},
		{
			name:    "missing client ID",
			config:  Config{IssuerURL: "https://example.com"},
			wantErr: true,
			errCode: ErrAdapterOIDCConfig,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr {
				require.Error(t, err)
				var ec *errcode.Error
				require.ErrorAs(t, err, &ec)
				assert.Equal(t, tt.errCode, ec.Code)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

func TestProvider_Discover(t *testing.T) {
	key := generateTestKey(t)
	server := testOIDCServer(t, key, "test-kid")
	defer server.Close()

	cfg := DefaultConfig(server.URL, "test-client", "test-secret")
	cfg.DiscoveryCacheTTL = 1 * time.Hour

	provider, err := NewProvider(cfg)
	require.NoError(t, err)

	ctx := context.Background()

	doc, err := provider.Discover(ctx)
	require.NoError(t, err)
	assert.Equal(t, server.URL, doc.Issuer)
	assert.Equal(t, server.URL+"/token", doc.TokenEndpoint)
	assert.Equal(t, server.URL+"/jwks", doc.JWKSURI)

	// Second call should use cache.
	doc2, err := provider.Discover(ctx)
	require.NoError(t, err)
	assert.Equal(t, doc.Issuer, doc2.Issuer)
}

func TestProvider_ExchangeCode(t *testing.T) {
	key := generateTestKey(t)
	server := testOIDCServer(t, key, "test-kid")
	defer server.Close()

	cfg := DefaultConfig(server.URL, "test-client", "test-secret")
	cfg.RedirectURL = "http://localhost/callback"

	provider, err := NewProvider(cfg)
	require.NoError(t, err)

	resp, err := provider.ExchangeCode(context.Background(), "test-auth-code")
	require.NoError(t, err)
	assert.Equal(t, "test-access-token", resp.AccessToken)
	assert.Equal(t, "Bearer", resp.TokenType)
	assert.Greater(t, resp.ExpiresIn, 0)
}

func TestVerifier_Verify(t *testing.T) {
	key := generateTestKey(t)
	kid := "test-kid-1"
	server := testOIDCServer(t, key, kid)
	defer server.Close()

	cfg := DefaultConfig(server.URL, "test-client", "test-secret")
	provider, err := NewProvider(cfg)
	require.NoError(t, err)

	verifier := NewVerifier(provider)

	// Create a valid ID token.
	now := time.Now()
	claims := jwt.MapClaims{
		"iss":   server.URL,
		"sub":   "user-456",
		"aud":   "test-client",
		"exp":   now.Add(1 * time.Hour).Unix(),
		"iat":   now.Unix(),
		"email": "user@example.com",
		"name":  "Test User",
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid

	rawToken, err := token.SignedString(key)
	require.NoError(t, err)

	result, err := verifier.Verify(context.Background(), rawToken)
	require.NoError(t, err)
	assert.Equal(t, "user-456", result.Subject)
	assert.Equal(t, server.URL, result.Issuer)
	assert.Equal(t, "user@example.com", result.Email)
	assert.Equal(t, "Test User", result.Name)
}

func TestVerifier_Verify_WrongAudience(t *testing.T) {
	key := generateTestKey(t)
	kid := "test-kid-2"
	server := testOIDCServer(t, key, kid)
	defer server.Close()

	cfg := DefaultConfig(server.URL, "test-client", "test-secret")
	provider, err := NewProvider(cfg)
	require.NoError(t, err)

	verifier := NewVerifier(provider)

	claims := jwt.MapClaims{
		"iss": server.URL,
		"sub": "user-789",
		"aud": "wrong-client",
		"exp": time.Now().Add(1 * time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid

	rawToken, err := token.SignedString(key)
	require.NoError(t, err)

	_, err = verifier.Verify(context.Background(), rawToken)
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterOIDCVerify, ec.Code)
}

func TestVerifier_Verify_ExpiredToken(t *testing.T) {
	key := generateTestKey(t)
	kid := "test-kid-3"
	server := testOIDCServer(t, key, kid)
	defer server.Close()

	cfg := DefaultConfig(server.URL, "test-client", "test-secret")
	provider, err := NewProvider(cfg)
	require.NoError(t, err)

	verifier := NewVerifier(provider)

	claims := jwt.MapClaims{
		"iss": server.URL,
		"sub": "user-expired",
		"aud": "test-client",
		"exp": time.Now().Add(-1 * time.Hour).Unix(),
		"iat": time.Now().Add(-2 * time.Hour).Unix(),
	}

	token := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	token.Header["kid"] = kid

	rawToken, err := token.SignedString(key)
	require.NoError(t, err)

	_, err = verifier.Verify(context.Background(), rawToken)
	require.Error(t, err)
}

func TestProvider_GetUserInfo(t *testing.T) {
	key := generateTestKey(t)
	server := testOIDCServer(t, key, "test-kid")
	defer server.Close()

	cfg := DefaultConfig(server.URL, "test-client", "test-secret")
	provider, err := NewProvider(cfg)
	require.NoError(t, err)

	info, err := provider.GetUserInfo(context.Background(), "test-access-token")
	require.NoError(t, err)
	assert.Equal(t, "user-123", info.Subject)
	assert.Equal(t, "Test User", info.Name)
	assert.Equal(t, "test@example.com", info.Email)
	assert.True(t, info.EmailVerified)
}

func TestProvider_GetUserInfo_Unauthorized(t *testing.T) {
	key := generateTestKey(t)
	server := testOIDCServer(t, key, "test-kid")
	defer server.Close()

	cfg := DefaultConfig(server.URL, "test-client", "test-secret")
	provider, err := NewProvider(cfg)
	require.NoError(t, err)

	_, err = provider.GetUserInfo(context.Background(), "bad-token")
	require.Error(t, err)

	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, ErrAdapterOIDCUserInfo, ec.Code)
}
