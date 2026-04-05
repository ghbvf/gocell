package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ---------- helpers ----------

// testKey generates a fresh RSA key pair for testing.
func testKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	return key
}

// jwksJSON builds a JWKS JSON response for the given public key and kid.
func jwksJSON(t *testing.T, pub *rsa.PublicKey, kid string) []byte {
	t.Helper()
	n := base64.RawURLEncoding.EncodeToString(pub.N.Bytes())
	e := base64.RawURLEncoding.EncodeToString(big.NewInt(int64(pub.E)).Bytes())
	j, err := json.Marshal(jwksResponse{
		Keys: []jwksKey{
			{
				KeyType:   "RSA",
				KeyID:     kid,
				Algorithm: "RS256",
				Use:       "sig",
				N:         n,
				E:         e,
			},
		},
	})
	require.NoError(t, err)
	return j
}

// signJWT creates a signed RS256 JWT with the given header and claims.
func signJWT(t *testing.T, key *rsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()

	header := map[string]string{"alg": "RS256", "kid": kid, "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
	require.NoError(t, err)
	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)

	claimsJSON, err := json.Marshal(claims)
	require.NoError(t, err)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)

	signingInput := headerB64 + "." + claimsB64
	hash := sha256.Sum256([]byte(signingInput))

	sig, err := rsa.SignPKCS1v15(rand.Reader, key, 0x05, hash[:]) // crypto.SHA256 = 5
	require.NoError(t, err)
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)

	return signingInput + "." + sigB64
}

// mockOIDCServer sets up an httptest.Server that serves Discovery, JWKS,
// Token, and UserInfo endpoints. It returns the server, a cleanup function,
// and functions to inspect requests.
type mockServer struct {
	Server *httptest.Server

	PrivateKey *rsa.PrivateKey
	KID        string

	// tokenHandler can be overridden in tests.
	tokenHandler    http.HandlerFunc
	userinfoHandler http.HandlerFunc
}

func newMockServer(t *testing.T) *mockServer {
	t.Helper()
	key := testKey(t)
	kid := "test-kid-1"

	ms := &mockServer{
		PrivateKey: key,
		KID:        kid,
	}

	mux := http.NewServeMux()

	// Discovery.
	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		issuer := ms.Server.URL
		resp := discoveryMetadata{
			Issuer:                issuer,
			AuthorizationEndpoint: issuer + "/authorize",
			TokenEndpoint:         issuer + "/token",
			UserInfoEndpoint:      issuer + "/userinfo",
			JWKSURI:               issuer + "/jwks",
			SupportedAlgorithms:   []string{"RS256"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	// JWKS.
	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write(jwksJSON(t, &key.PublicKey, kid))
	})

	// Token endpoint.
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		if ms.tokenHandler != nil {
			ms.tokenHandler(w, r)
			return
		}
		// Default: return a valid token response.
		claims := map[string]any{
			"iss": ms.Server.URL,
			"sub": "user-123",
			"aud": "test-client-id",
			"exp": time.Now().Add(time.Hour).Unix(),
			"iat": time.Now().Unix(),
		}
		idToken := signJWT(t, key, kid, claims)
		resp := TokenResponse{
			AccessToken:  "mock-access-token",
			TokenType:    "Bearer",
			IDToken:      idToken,
			RefreshToken: "mock-refresh-token",
			ExpiresIn:    3600,
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	// UserInfo endpoint.
	mux.HandleFunc("GET /userinfo", func(w http.ResponseWriter, r *http.Request) {
		if ms.userinfoHandler != nil {
			ms.userinfoHandler(w, r)
			return
		}
		authHeader := r.Header.Get("Authorization")
		if authHeader == "" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		resp := map[string]any{
			"sub":            "user-123",
			"name":           "Test User",
			"email":          "test@example.com",
			"email_verified": true,
			"picture":        "https://example.com/photo.jpg",
			"locale":         "en",
			"custom_claim":   "custom_value",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	ms.Server = httptest.NewServer(mux)
	t.Cleanup(ms.Server.Close)

	return ms
}

func (ms *mockServer) config() Config {
	return Config{
		IssuerURL:    ms.Server.URL,
		ClientID:     "test-client-id",
		ClientSecret: "test-client-secret",
		RedirectURL:  "http://localhost/callback",
	}
}

// ---------- Config tests ----------

func TestConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		cfg     Config
		wantErr bool
		errMsg  string
	}{
		{
			name: "valid config",
			cfg: Config{
				IssuerURL:    "https://issuer.example.com",
				ClientID:     "client-id",
				ClientSecret: "client-secret",
				RedirectURL:  "https://app.example.com/callback",
			},
			wantErr: false,
		},
		{
			name: "missing issuer URL",
			cfg: Config{
				ClientID:     "client-id",
				ClientSecret: "client-secret",
				RedirectURL:  "https://app.example.com/callback",
			},
			wantErr: true,
			errMsg:  "issuer URL is required",
		},
		{
			name: "missing client ID",
			cfg: Config{
				IssuerURL:    "https://issuer.example.com",
				ClientSecret: "client-secret",
				RedirectURL:  "https://app.example.com/callback",
			},
			wantErr: true,
			errMsg:  "client ID is required",
		},
		{
			name: "missing client secret",
			cfg: Config{
				IssuerURL:   "https://issuer.example.com",
				ClientID:    "client-id",
				RedirectURL: "https://app.example.com/callback",
			},
			wantErr: true,
			errMsg:  "client secret is required",
		},
		{
			name: "missing redirect URL",
			cfg: Config{
				IssuerURL:    "https://issuer.example.com",
				ClientID:     "client-id",
				ClientSecret: "client-secret",
			},
			wantErr: true,
			errMsg:  "redirect URL is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.cfg.Validate()
			if tt.wantErr {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.errMsg)
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// ---------- Provider / Discovery tests ----------

func TestProvider_Discovery(t *testing.T) {
	ms := newMockServer(t)
	ctx := context.Background()

	p, err := NewProvider(ctx, ms.config())
	require.NoError(t, err)

	md, err := p.Metadata(ctx)
	require.NoError(t, err)

	assert.Equal(t, ms.Server.URL, md.Issuer)
	assert.Equal(t, ms.Server.URL+"/authorize", md.AuthorizationEndpoint)
	assert.Equal(t, ms.Server.URL+"/token", md.TokenEndpoint)
	assert.Equal(t, ms.Server.URL+"/userinfo", md.UserInfoEndpoint)
	assert.Equal(t, ms.Server.URL+"/jwks", md.JWKSURI)
	assert.Equal(t, []string{"RS256"}, md.SupportedAlgorithms)
}

func TestProvider_Discovery_Failure(t *testing.T) {
	// Server that returns 500.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		IssuerURL:    srv.URL,
		ClientID:     "id",
		ClientSecret: "secret",
		RedirectURL:  "http://localhost/cb",
	}
	ctx := context.Background()

	_, err := NewProvider(ctx, cfg)
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrDiscovery, ecErr.Code)
}

func TestProvider_Discovery_InvalidJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("not json"))
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		IssuerURL:    srv.URL,
		ClientID:     "id",
		ClientSecret: "secret",
		RedirectURL:  "http://localhost/cb",
	}
	ctx := context.Background()

	_, err := NewProvider(ctx, cfg)
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrDiscovery, ecErr.Code)
}

func TestProvider_Discovery_MissingFields(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Missing issuer, jwks_uri, token_endpoint.
		json.NewEncoder(w).Encode(map[string]string{
			"authorization_endpoint": "https://example.com/auth",
		})
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		IssuerURL:    srv.URL,
		ClientID:     "id",
		ClientSecret: "secret",
		RedirectURL:  "http://localhost/cb",
	}
	ctx := context.Background()

	_, err := NewProvider(ctx, cfg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing required fields")
}

func TestProvider_MetadataCache(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		md := discoveryMetadata{
			Issuer:                "https://issuer.example.com",
			AuthorizationEndpoint: "https://issuer.example.com/auth",
			TokenEndpoint:         "https://issuer.example.com/token",
			JWKSURI:               "https://issuer.example.com/jwks",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(md)
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		IssuerURL:    srv.URL,
		ClientID:     "id",
		ClientSecret: "secret",
		RedirectURL:  "http://localhost/cb",
	}
	ctx := context.Background()

	p, err := NewProvider(ctx, cfg)
	require.NoError(t, err)
	assert.Equal(t, 1, callCount, "discovery should be called once on init")

	// Second call should use cache.
	_, err = p.Metadata(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, callCount, "discovery should not be called again within cache TTL")
}

func TestProvider_Health(t *testing.T) {
	ms := newMockServer(t)
	ctx := context.Background()

	p, err := NewProvider(ctx, ms.config())
	require.NoError(t, err)

	err = p.Health(ctx)
	require.NoError(t, err)
}

func TestProvider_Health_Failure(t *testing.T) {
	ms := newMockServer(t)
	ctx := context.Background()

	p, err := NewProvider(ctx, ms.config())
	require.NoError(t, err)

	// Stop server to simulate failure.
	ms.Server.Close()

	err = p.Health(ctx)
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrDiscovery, ecErr.Code)
}

// ---------- Token Exchange tests ----------

func TestExchangeCode_Success(t *testing.T) {
	ms := newMockServer(t)
	ctx := context.Background()

	p, err := NewProvider(ctx, ms.config())
	require.NoError(t, err)

	tr, err := p.ExchangeCode(ctx, "auth-code-123", "http://localhost/callback")
	require.NoError(t, err)

	assert.Equal(t, "mock-access-token", tr.AccessToken)
	assert.Equal(t, "Bearer", tr.TokenType)
	assert.NotEmpty(t, tr.IDToken)
	assert.Equal(t, "mock-refresh-token", tr.RefreshToken)
	assert.Equal(t, 3600, tr.ExpiresIn)
}

func TestExchangeCode_RequestParams(t *testing.T) {
	ms := newMockServer(t)

	var capturedCode, capturedRedirect, capturedGrantType string
	ms.tokenHandler = func(w http.ResponseWriter, r *http.Request) {
		err := r.ParseForm()
		require.NoError(t, err)
		capturedCode = r.FormValue("code")
		capturedRedirect = r.FormValue("redirect_uri")
		capturedGrantType = r.FormValue("grant_type")

		resp := TokenResponse{AccessToken: "tok", TokenType: "Bearer", ExpiresIn: 3600}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}

	ctx := context.Background()
	p, err := NewProvider(ctx, ms.config())
	require.NoError(t, err)

	_, err = p.ExchangeCode(ctx, "my-code", "http://myapp/cb")
	require.NoError(t, err)

	assert.Equal(t, "my-code", capturedCode)
	assert.Equal(t, "http://myapp/cb", capturedRedirect)
	assert.Equal(t, "authorization_code", capturedGrantType)
}

func TestExchangeCode_ErrorResponse(t *testing.T) {
	ms := newMockServer(t)
	ms.tokenHandler = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		w.Write([]byte(`{"error":"invalid_grant"}`))
	}

	ctx := context.Background()
	p, err := NewProvider(ctx, ms.config())
	require.NoError(t, err)

	_, err = p.ExchangeCode(ctx, "bad-code", "http://localhost/callback")
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrTokenExchange, ecErr.Code)
	assert.Contains(t, ecErr.Message, "HTTP 400")
}

func TestExchangeCode_MissingAccessToken(t *testing.T) {
	ms := newMockServer(t)
	ms.tokenHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"token_type":"Bearer"}`))
	}

	ctx := context.Background()
	p, err := NewProvider(ctx, ms.config())
	require.NoError(t, err)

	_, err = p.ExchangeCode(ctx, "code", "http://localhost/callback")
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrTokenExchange, ecErr.Code)
	assert.Contains(t, ecErr.Message, "missing access_token")
}

// ---------- Verifier tests ----------

func TestVerifier_VerifyValid(t *testing.T) {
	ms := newMockServer(t)
	ctx := context.Background()

	p, err := NewProvider(ctx, ms.config())
	require.NoError(t, err)

	v, err := NewVerifier(ctx, p)
	require.NoError(t, err)

	claims := map[string]any{
		"iss":   ms.Server.URL,
		"sub":   "user-456",
		"aud":   "test-client-id",
		"exp":   time.Now().Add(time.Hour).Unix(),
		"iat":   time.Now().Unix(),
		"email": "test@example.com",
		"nonce": "abc",
	}
	token := signJWT(t, ms.PrivateKey, ms.KID, claims)

	result, err := v.Verify(ctx, token)
	require.NoError(t, err)

	assert.Equal(t, "user-456", result.Subject)
	assert.Equal(t, ms.Server.URL, result.Issuer)
	assert.Equal(t, []string{"test-client-id"}, result.Audience)
	assert.Equal(t, "test@example.com", result.Extra["email"])
	assert.Equal(t, "abc", result.Extra["nonce"])
	assert.True(t, result.ExpiresAt.After(time.Now()))
}

func TestVerifier_VerifyAudienceArray(t *testing.T) {
	ms := newMockServer(t)
	ctx := context.Background()

	p, err := NewProvider(ctx, ms.config())
	require.NoError(t, err)

	v, err := NewVerifier(ctx, p)
	require.NoError(t, err)

	claims := map[string]any{
		"iss": ms.Server.URL,
		"sub": "user-789",
		"aud": []string{"other-client", "test-client-id"},
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	token := signJWT(t, ms.PrivateKey, ms.KID, claims)

	result, err := v.Verify(ctx, token)
	require.NoError(t, err)
	assert.Equal(t, "user-789", result.Subject)
	assert.Contains(t, result.Audience, "test-client-id")
}

func TestVerifier_ExpiredToken(t *testing.T) {
	ms := newMockServer(t)
	ctx := context.Background()

	p, err := NewProvider(ctx, ms.config())
	require.NoError(t, err)

	v, err := NewVerifier(ctx, p)
	require.NoError(t, err)

	claims := map[string]any{
		"iss": ms.Server.URL,
		"sub": "user-expired",
		"aud": "test-client-id",
		"exp": time.Now().Add(-time.Hour).Unix(), // expired
		"iat": time.Now().Add(-2 * time.Hour).Unix(),
	}
	token := signJWT(t, ms.PrivateKey, ms.KID, claims)

	_, err = v.Verify(ctx, token)
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrTokenVerify, ecErr.Code)
	assert.Contains(t, ecErr.Message, "expired")
}

func TestVerifier_WrongIssuer(t *testing.T) {
	ms := newMockServer(t)
	ctx := context.Background()

	p, err := NewProvider(ctx, ms.config())
	require.NoError(t, err)

	v, err := NewVerifier(ctx, p)
	require.NoError(t, err)

	claims := map[string]any{
		"iss": "https://evil-issuer.example.com",
		"sub": "user-evil",
		"aud": "test-client-id",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	token := signJWT(t, ms.PrivateKey, ms.KID, claims)

	_, err = v.Verify(ctx, token)
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrTokenVerify, ecErr.Code)
	assert.Contains(t, ecErr.Message, "issuer mismatch")
}

func TestVerifier_WrongAudience(t *testing.T) {
	ms := newMockServer(t)
	ctx := context.Background()

	p, err := NewProvider(ctx, ms.config())
	require.NoError(t, err)

	v, err := NewVerifier(ctx, p)
	require.NoError(t, err)

	claims := map[string]any{
		"iss": ms.Server.URL,
		"sub": "user-wrong-aud",
		"aud": "wrong-client-id",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	token := signJWT(t, ms.PrivateKey, ms.KID, claims)

	_, err = v.Verify(ctx, token)
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrTokenVerify, ecErr.Code)
	assert.Contains(t, ecErr.Message, "not found in audience")
}

func TestVerifier_InvalidTokenFormat(t *testing.T) {
	ms := newMockServer(t)
	ctx := context.Background()

	p, err := NewProvider(ctx, ms.config())
	require.NoError(t, err)

	v, err := NewVerifier(ctx, p)
	require.NoError(t, err)

	_, err = v.Verify(ctx, "not-a-jwt")
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrTokenVerify, ecErr.Code)
	assert.Contains(t, ecErr.Message, "3 parts")
}

func TestVerifier_UnsupportedAlgorithm(t *testing.T) {
	ms := newMockServer(t)
	ctx := context.Background()

	p, err := NewProvider(ctx, ms.config())
	require.NoError(t, err)

	v, err := NewVerifier(ctx, p)
	require.NoError(t, err)

	// Craft a token with HS256 header.
	header := map[string]string{"alg": "HS256", "kid": ms.KID, "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
	require.NoError(t, err)
	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)

	claimsJSON, err := json.Marshal(map[string]any{"sub": "x"})
	require.NoError(t, err)
	claimsB64 := base64.RawURLEncoding.EncodeToString(claimsJSON)

	fakeToken := headerB64 + "." + claimsB64 + ".fake-sig"

	_, err = v.Verify(ctx, fakeToken)
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrTokenVerify, ecErr.Code)
	assert.Contains(t, ecErr.Message, "unsupported signing algorithm")
}

func TestVerifier_TamperedSignature(t *testing.T) {
	ms := newMockServer(t)
	ctx := context.Background()

	p, err := NewProvider(ctx, ms.config())
	require.NoError(t, err)

	v, err := NewVerifier(ctx, p)
	require.NoError(t, err)

	claims := map[string]any{
		"iss": ms.Server.URL,
		"sub": "user-tampered",
		"aud": "test-client-id",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	token := signJWT(t, ms.PrivateKey, ms.KID, claims)

	// Tamper with the payload: replace the claims segment with a different one,
	// keeping the original header and signature. This reliably breaks verification.
	parts := strings.SplitN(token, ".", 3)
	alteredClaims := map[string]any{
		"iss": ms.Server.URL,
		"sub": "user-EVIL",
		"aud": "test-client-id",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	alteredJSON, err := json.Marshal(alteredClaims)
	require.NoError(t, err)
	alteredB64 := base64.RawURLEncoding.EncodeToString(alteredJSON)
	tampered := parts[0] + "." + alteredB64 + "." + parts[2]

	_, err = v.Verify(ctx, tampered)
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrTokenVerify, ecErr.Code)
	assert.Contains(t, ecErr.Message, "signature verification failed")
}

func TestVerifier_KIDRotation(t *testing.T) {
	// Start with one key, then rotate to another.
	key1 := testKey(t)
	key2 := testKey(t)
	currentKID := "kid-v1"
	currentKey := key1

	mux := http.NewServeMux()

	mux.HandleFunc("GET /.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
		// We need a self-referencing URL, but we don't have the server URL yet.
		// Use the Host header.
		base := "http://" + r.Host
		md := discoveryMetadata{
			Issuer:                base,
			AuthorizationEndpoint: base + "/authorize",
			TokenEndpoint:         base + "/token",
			JWKSURI:               base + "/jwks",
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(md)
	})

	mux.HandleFunc("GET /jwks", func(w http.ResponseWriter, r *http.Request) {
		// Return only the current key.
		w.Header().Set("Content-Type", "application/json")
		w.Write(jwksJSON(t, &currentKey.PublicKey, currentKID))
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	cfg := Config{
		IssuerURL:    srv.URL,
		ClientID:     "test-client-id",
		ClientSecret: "secret",
		RedirectURL:  "http://localhost/cb",
	}
	ctx := context.Background()

	p, err := NewProvider(ctx, cfg)
	require.NoError(t, err)

	v, err := NewVerifier(ctx, p)
	require.NoError(t, err)

	// Sign a token with key1/kid-v1 -- should verify.
	claims := map[string]any{
		"iss": srv.URL,
		"sub": "user-1",
		"aud": "test-client-id",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	token1 := signJWT(t, key1, "kid-v1", claims)
	_, err = v.Verify(ctx, token1)
	require.NoError(t, err)

	// Rotate: now the JWKS serves key2 under kid-v2.
	currentKID = "kid-v2"
	currentKey = key2

	// Sign a token with key2/kid-v2 -- verifier should refresh JWKS on kid miss.
	claims["sub"] = "user-2"
	token2 := signJWT(t, key2, "kid-v2", claims)
	result, err := v.Verify(ctx, token2)
	require.NoError(t, err)
	assert.Equal(t, "user-2", result.Subject)
}

func TestVerifier_UnknownKID(t *testing.T) {
	ms := newMockServer(t)
	ctx := context.Background()

	p, err := NewProvider(ctx, ms.config())
	require.NoError(t, err)

	v, err := NewVerifier(ctx, p)
	require.NoError(t, err)

	// Sign with a key that will never appear in JWKS.
	otherKey := testKey(t)
	claims := map[string]any{
		"iss": ms.Server.URL,
		"sub": "user-unknown",
		"aud": "test-client-id",
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}
	token := signJWT(t, otherKey, "unknown-kid", claims)

	_, err = v.Verify(ctx, token)
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrTokenVerify, ecErr.Code)
	assert.Contains(t, ecErr.Message, "not found in JWKS")
}

// ---------- UserInfo tests ----------

func TestUserInfo_Success(t *testing.T) {
	ms := newMockServer(t)
	ctx := context.Background()

	p, err := NewProvider(ctx, ms.config())
	require.NoError(t, err)

	uir, err := p.UserInfo(ctx, "valid-access-token")
	require.NoError(t, err)

	assert.Equal(t, "user-123", uir.Subject)
	assert.Equal(t, "Test User", uir.Name)
	assert.Equal(t, "test@example.com", uir.Email)
	assert.True(t, uir.EmailVerified)
	assert.Equal(t, "https://example.com/photo.jpg", uir.Picture)
	assert.Equal(t, "en", uir.Locale)
	assert.Equal(t, "custom_value", uir.Extra["custom_claim"])
}

func TestUserInfo_AuthorizationHeader(t *testing.T) {
	ms := newMockServer(t)

	var capturedAuth string
	ms.userinfoHandler = func(w http.ResponseWriter, r *http.Request) {
		capturedAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"sub":  "user-123",
			"name": "Test",
		})
	}

	ctx := context.Background()
	p, err := NewProvider(ctx, ms.config())
	require.NoError(t, err)

	_, err = p.UserInfo(ctx, "my-access-token")
	require.NoError(t, err)

	assert.Equal(t, "Bearer my-access-token", capturedAuth)
}

func TestUserInfo_Unauthorized(t *testing.T) {
	ms := newMockServer(t)
	ms.userinfoHandler = func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":"invalid_token"}`))
	}

	ctx := context.Background()
	p, err := NewProvider(ctx, ms.config())
	require.NoError(t, err)

	_, err = p.UserInfo(ctx, "bad-token")
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrUserInfo, ecErr.Code)
	assert.Contains(t, ecErr.Message, "HTTP 401")
}

func TestUserInfo_MissingSub(t *testing.T) {
	ms := newMockServer(t)
	ms.userinfoHandler = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{
			"name":  "No Sub User",
			"email": "nosub@example.com",
		})
	}

	ctx := context.Background()
	p, err := NewProvider(ctx, ms.config())
	require.NoError(t, err)

	_, err = p.UserInfo(ctx, "token")
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrUserInfo, ecErr.Code)
	assert.Contains(t, ecErr.Message, "missing sub")
}

func TestUserInfo_NoEndpoint(t *testing.T) {
	// Mock server without userinfo endpoint in discovery.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "http://" + r.Host
		md := discoveryMetadata{
			Issuer:        base,
			TokenEndpoint: base + "/token",
			JWKSURI:       base + "/jwks",
			// Intentionally omit UserInfoEndpoint.
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(md)
	}))
	t.Cleanup(srv.Close)

	cfg := Config{
		IssuerURL:    srv.URL,
		ClientID:     "id",
		ClientSecret: "secret",
		RedirectURL:  "http://localhost/cb",
	}
	ctx := context.Background()

	p, err := NewProvider(ctx, cfg)
	require.NoError(t, err)

	_, err = p.UserInfo(ctx, "token")
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, ErrUserInfo, ecErr.Code)
	assert.Contains(t, ecErr.Message, "does not expose a userinfo endpoint")
}

// ---------- Crypto helper tests ----------

func TestVerifyRS256(t *testing.T) {
	key := testKey(t)

	message := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0"
	hash := sha256.Sum256([]byte(message))
	sig, err := rsa.SignPKCS1v15(rand.Reader, key, 0x05, hash[:])
	require.NoError(t, err)
	sigB64 := base64.RawURLEncoding.EncodeToString(sig)

	err = verifyRS256(message, sigB64, &key.PublicKey)
	require.NoError(t, err)
}

func TestVerifyRS256_BadSignature(t *testing.T) {
	key := testKey(t)

	message := "eyJhbGciOiJSUzI1NiJ9.eyJzdWIiOiJ0ZXN0In0"
	err := verifyRS256(message, "badsignature", &key.PublicKey)
	require.Error(t, err)
}

// ---------- Audience unmarshal tests ----------

func TestAudience_UnmarshalString(t *testing.T) {
	var a audience
	err := json.Unmarshal([]byte(`"single-audience"`), &a)
	require.NoError(t, err)
	assert.Equal(t, audience{"single-audience"}, a)
}

func TestAudience_UnmarshalArray(t *testing.T) {
	var a audience
	err := json.Unmarshal([]byte(`["aud1","aud2"]`), &a)
	require.NoError(t, err)
	assert.Equal(t, audience{"aud1", "aud2"}, a)
}

func TestAudience_UnmarshalInvalid(t *testing.T) {
	var a audience
	err := json.Unmarshal([]byte(`123`), &a)
	require.Error(t, err)
}

// ---------- truncate tests ----------

func TestTruncate(t *testing.T) {
	assert.Equal(t, "short", truncate([]byte("short"), 10))
	assert.Equal(t, "12345...", truncate([]byte("1234567890"), 5))
}

// ---------- WithHTTPClient option test ----------

func TestWithHTTPClient(t *testing.T) {
	ms := newMockServer(t)
	ctx := context.Background()

	customClient := &http.Client{Timeout: 5 * time.Second}
	p, err := NewProvider(ctx, ms.config(), WithHTTPClient(customClient))
	require.NoError(t, err)

	assert.Equal(t, customClient, p.client)

	// Verify it still works.
	md, err := p.Metadata(ctx)
	require.NoError(t, err)
	assert.Equal(t, ms.Server.URL, md.Issuer)
}

// ---------- Interface compliance ----------

// Compile-time check that Verifier implements auth.TokenVerifier.
var _ interface {
	Verify(context.Context, string) (auth.Claims, error)
} = (*Verifier)(nil)
