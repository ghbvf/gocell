package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustTestRing(t *testing.T, current, previous string) *HMACKeyRing {
	t.Helper()
	var prev []byte
	if previous != "" {
		prev = []byte(previous)
	}
	ring, err := NewHMACKeyRing([]byte(current), prev)
	require.NoError(t, err)
	return ring
}

// --- Phase 4: User Story 3 (T017-T025) ---

func TestHMACKeyRing_SignWithCurrent(t *testing.T) {
	ring := mustTestRing(t, "new-secret", "old-secret")

	now := time.Now()
	token := GenerateServiceToken(ring, http.MethodGet, "/api", now)

	// Should be verifiable with current secret only.
	singleRing := mustTestRing(t, "new-secret", "")
	handler := ServiceTokenMiddleware(singleRing)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	origNow := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = origNow }()

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHMACKeyRing_VerifyWithPrevious(t *testing.T) {
	now := time.Now()
	origNow := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = origNow }()

	// Sign token with old secret.
	oldRing := mustTestRing(t, "old-secret", "")
	token := GenerateServiceToken(oldRing, http.MethodGet, "/api", now)

	// Create ring with new+old. Old token should still verify.
	newRing := mustTestRing(t, "new-secret", "old-secret")
	handler := ServiceTokenMiddleware(newRing)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHMACKeyRing_RejectUnknownSecret(t *testing.T) {
	now := time.Now()
	origNow := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = origNow }()

	// Sign with a secret that is NOT in the ring.
	unknownRing := mustTestRing(t, "unknown-secret", "")
	token := GenerateServiceToken(unknownRing, http.MethodGet, "/api", now)

	ring := mustTestRing(t, "new-secret", "old-secret")
	handler := ServiceTokenMiddleware(ring)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHMACKeyRing_SingleSecretMode(t *testing.T) {
	now := time.Now()
	origNow := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = origNow }()

	ring := mustTestRing(t, "only-secret", "")
	token := GenerateServiceToken(ring, http.MethodGet, "/api", now)

	handler := ServiceTokenMiddleware(ring)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHMACKeyRing_SameSecretBothPositions(t *testing.T) {
	now := time.Now()
	origNow := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = origNow }()

	ring := mustTestRing(t, "same-secret", "same-secret")
	token := GenerateServiceToken(ring, http.MethodGet, "/api", now)

	handler := ServiceTokenMiddleware(ring)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestNewHMACKeyRing_EmptyCurrentFails(t *testing.T) {
	_, err := NewHMACKeyRing(nil, nil)
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrAuthKeyMissing, ecErr.Code)
}

func TestHMACKeyRing_Secrets_SingleKey(t *testing.T) {
	ring := mustTestRing(t, "secret", "")
	secrets := ring.Secrets()
	assert.Len(t, secrets, 1)
	assert.Equal(t, []byte("secret"), secrets[0])
}

func TestHMACKeyRing_Secrets_DualKey(t *testing.T) {
	ring := mustTestRing(t, "new", "old")
	secrets := ring.Secrets()
	assert.Len(t, secrets, 2)
	assert.Equal(t, []byte("new"), secrets[0])
	assert.Equal(t, []byte("old"), secrets[1])
}

func TestLoadHMACKeyRingFromEnv_CurrentOnly(t *testing.T) {
	t.Setenv(EnvServiceSecret, "my-secret")
	t.Setenv(EnvServiceSecretPrevious, "")

	ring, err := LoadHMACKeyRingFromEnv()
	require.NoError(t, err)
	assert.Equal(t, []byte("my-secret"), ring.Current())
	assert.Len(t, ring.Secrets(), 1)
}

func TestLoadHMACKeyRingFromEnv_CurrentAndPrevious(t *testing.T) {
	t.Setenv(EnvServiceSecret, "new-secret")
	t.Setenv(EnvServiceSecretPrevious, "old-secret")

	ring, err := LoadHMACKeyRingFromEnv()
	require.NoError(t, err)
	assert.Equal(t, []byte("new-secret"), ring.Current())
	assert.Len(t, ring.Secrets(), 2)
}

func TestLoadHMACKeyRingFromEnv_MissingCurrentFails(t *testing.T) {
	t.Setenv(EnvServiceSecret, "")
	t.Setenv(EnvServiceSecretPrevious, "")

	_, err := LoadHMACKeyRingFromEnv()
	require.Error(t, err)

	var ecErr *errcode.Error
	require.True(t, errors.As(err, &ecErr))
	assert.Equal(t, errcode.ErrAuthKeyMissing, ecErr.Code)
}

// --- Updated existing tests ---

func TestServiceTokenMiddleware_ValidToken(t *testing.T) {
	ring := mustTestRing(t, "test-secret", "")
	handler := ServiceTokenMiddleware(ring)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	now := time.Now()
	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/health", now)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)

	origNow := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = origNow }()

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestServiceTokenMiddleware_InvalidToken(t *testing.T) {
	ring := mustTestRing(t, "test-secret", "")
	handler := ServiceTokenMiddleware(ring)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	ring := mustTestRing(t, "test-secret", "")
	handler := ServiceTokenMiddleware(ring)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_WrongScheme(t *testing.T) {
	ring := mustTestRing(t, "test-secret", "")
	handler := ServiceTokenMiddleware(ring)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer some-jwt")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_DifferentPath(t *testing.T) {
	ring := mustTestRing(t, "test-secret", "")
	handler := ServiceTokenMiddleware(ring)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	now := time.Now()
	origNow := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = origNow }()

	token := GenerateServiceToken(ring, http.MethodGet, "/other", now)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_NilRing(t *testing.T) {
	handler := ServiceTokenMiddleware(nil)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestServiceTokenMiddleware_ExpiredTimestamp(t *testing.T) {
	ring := mustTestRing(t, "test-secret", "")
	handler := ServiceTokenMiddleware(ring)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	now := time.Now()
	origNow := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = origNow }()

	oldTime := now.Add(-6 * time.Minute)
	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/health", oldTime)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_ExactBoundary_Rejected(t *testing.T) {
	ring := mustTestRing(t, "test-secret", "")
	handler := ServiceTokenMiddleware(ring)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	now := time.Now()
	origNow := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = origNow }()

	boundaryTime := now.Add(-ServiceTokenMaxAge)
	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/health", boundaryTime)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_JustWithinWindow(t *testing.T) {
	ring := mustTestRing(t, "test-secret", "")
	handler := ServiceTokenMiddleware(ring)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	now := time.Now()
	origNow := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = origNow }()

	recentTime := now.Add(-4*time.Minute - 59*time.Second)
	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/health", recentTime)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestServiceTokenMiddleware_FutureTimestamp_Rejected(t *testing.T) {
	ring := mustTestRing(t, "test-secret", "")
	handler := ServiceTokenMiddleware(ring)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	now := time.Now()
	origNow := nowFunc
	nowFunc = func() time.Time { return now }
	defer func() { nowFunc = origNow }()

	futureTime := now.Add(6 * time.Minute)
	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/health", futureTime)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_InvalidFormat_NoColon(t *testing.T) {
	ring := mustTestRing(t, "test-secret", "")
	handler := ServiceTokenMiddleware(ring)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "ServiceToken nocolonhere")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestGenerateServiceToken_Deterministic(t *testing.T) {
	ring := mustTestRing(t, "test-secret", "")
	ts := time.Unix(1700000000, 0)
	t1 := GenerateServiceToken(ring, http.MethodPost, "/api", ts)
	t2 := GenerateServiceToken(ring, http.MethodPost, "/api", ts)
	assert.Equal(t, t1, t2)

	// Different method should produce different token.
	t3 := GenerateServiceToken(ring, http.MethodGet, "/api", ts)
	assert.NotEqual(t, t1, t3)
}
