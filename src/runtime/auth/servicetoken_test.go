package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestServiceTokenMiddleware_ValidToken(t *testing.T) {
	secret := []byte("test-secret")
	handler := ServiceTokenMiddleware(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	now := time.Now()
	token := GenerateServiceToken(secret, http.MethodGet, "/internal/v1/health", now)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)

	// Override nowFunc so it matches the token timestamp.
	origNow := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = origNow }()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestServiceTokenMiddleware_InvalidToken(t *testing.T) {
	secret := []byte("test-secret")
	handler := ServiceTokenMiddleware(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	now := time.Now()
	origNow := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = origNow }()

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken 12345:deadbeef")
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

func TestServiceTokenMiddleware_DifferentPath(t *testing.T) {
	secret := []byte("test-secret")
	handler := ServiceTokenMiddleware(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	now := time.Now()
	origNow := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = origNow }()

	// Token computed for different path.
	token := GenerateServiceToken(secret, http.MethodGet, "/other", now)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_EmptySecret(t *testing.T) {
	handler := ServiceTokenMiddleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestServiceTokenMiddleware_ExpiredTimestamp(t *testing.T) {
	secret := []byte("test-secret")
	handler := ServiceTokenMiddleware(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	now := time.Now()
	origNow := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = origNow }()

	// Token with timestamp 6 minutes ago (exceeds 5 min window).
	oldTime := now.Add(-6 * time.Minute)
	token := GenerateServiceToken(secret, http.MethodGet, "/internal/v1/health", oldTime)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_ExactBoundary_Rejected(t *testing.T) {
	secret := []byte("test-secret")
	handler := ServiceTokenMiddleware(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	now := time.Now()
	origNow := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = origNow }()

	// Token with timestamp exactly 5 minutes ago (boundary = rejected).
	boundaryTime := now.Add(-ServiceTokenMaxAge)
	token := GenerateServiceToken(secret, http.MethodGet, "/internal/v1/health", boundaryTime)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_JustWithinWindow(t *testing.T) {
	secret := []byte("test-secret")
	handler := ServiceTokenMiddleware(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	now := time.Now()
	origNow := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = origNow }()

	// Token with timestamp 4 minutes 59 seconds ago (within window).
	recentTime := now.Add(-4*time.Minute - 59*time.Second)
	token := GenerateServiceToken(secret, http.MethodGet, "/internal/v1/health", recentTime)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestServiceTokenMiddleware_FutureTimestamp_Rejected(t *testing.T) {
	secret := []byte("test-secret")
	handler := ServiceTokenMiddleware(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	now := time.Now()
	origNow := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = origNow }()

	// Token with timestamp 6 minutes in the future.
	futureTime := now.Add(6 * time.Minute)
	token := GenerateServiceToken(secret, http.MethodGet, "/internal/v1/health", futureTime)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_InvalidFormat_NoColon(t *testing.T) {
	secret := []byte("test-secret")
	handler := ServiceTokenMiddleware(secret)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "ServiceToken nocolonhere")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestGenerateServiceToken_Deterministic(t *testing.T) {
	secret := []byte("test-secret")
	ts := time.Unix(1700000000, 0)
	t1 := GenerateServiceToken(secret, http.MethodPost, "/api", ts)
	t2 := GenerateServiceToken(secret, http.MethodPost, "/api", ts)
	assert.Equal(t, t1, t2)

	// Different method should produce different token.
	t3 := GenerateServiceToken(secret, http.MethodGet, "/api", ts)
	assert.NotEqual(t, t1, t3)
}
