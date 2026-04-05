package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func computeServiceToken(secret []byte, method, path string) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write([]byte(method + " " + path))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestServiceTokenMiddleware_ValidToken(t *testing.T) {
	secret := []byte("test-secret")
	handler := ServiceTokenMiddleware(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	token := computeServiceToken(secret, http.MethodGet, "/internal/v1/health")
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestServiceTokenMiddleware_InvalidToken(t *testing.T) {
	secret := []byte("test-secret")
	handler := ServiceTokenMiddleware(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken deadbeef")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_MissingToken(t *testing.T) {
	secret := []byte("test-secret")
	handler := ServiceTokenMiddleware(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_WrongScheme(t *testing.T) {
	secret := []byte("test-secret")
	handler := ServiceTokenMiddleware(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer some-jwt")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_InvalidHex(t *testing.T) {
	secret := []byte("test-secret")
	handler := ServiceTokenMiddleware(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "ServiceToken not-hex!")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_DifferentPath(t *testing.T) {
	secret := []byte("test-secret")
	handler := ServiceTokenMiddleware(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	// Token computed for different path.
	token := computeServiceToken(secret, http.MethodGet, "/other")
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
