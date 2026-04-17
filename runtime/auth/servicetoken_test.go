package auth

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// test secrets — each exactly 32 bytes (>= MinHMACKeyBytes).
const (
	testSecret    = "test-secret-padding-to-32bytes!!" // len=32
	testSecretOld = "old--secret-padding-to-32bytes!!" // len=32
	testSecretNew = "new--secret-padding-to-32bytes!!" // len=32
	testSecretUnk = "unkn-secret-padding-to-32bytes!!" // len=32
	testSecretOne = "only-secret-padding-to-32bytes!!" // len=32
	testSecretSam = "same-secret-padding-to-32bytes!!" // len=32
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

func mustTestServiceHandler(t *testing.T, ring *HMACKeyRing, clockFn func() time.Time) http.Handler {
	t.Helper()
	return ServiceTokenMiddleware(ring, WithServiceTokenClock(clockFn))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)
}

func mustTestServiceHandlerFatal(t *testing.T, ring *HMACKeyRing, clockFn func() time.Time) http.Handler {
	t.Helper()
	return ServiceTokenMiddleware(ring, WithServiceTokenClock(clockFn))(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("should not be called")
		}),
	)
}

func TestHMACKeyRing_Current_ReturnsCopy(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	c := ring.Current()
	original := make([]byte, len(c))
	copy(original, c)

	c[0] = 0xFF

	assert.Equal(t, original, ring.Current(), "Current() must return a defensive copy")
}

func TestHMACKeyRing_SignWithCurrent(t *testing.T) {
	ring := mustTestRing(t, testSecretNew, testSecretOld)
	now := time.Now()
	token := GenerateServiceToken(ring, http.MethodGet, "/api", "", now)

	singleRing := mustTestRing(t, testSecretNew, "")
	handler := mustTestServiceHandler(t, singleRing, func() time.Time { return now })

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHMACKeyRing_VerifyWithPrevious(t *testing.T) {
	now := time.Now()

	oldRing := mustTestRing(t, testSecretOld, "")
	token := GenerateServiceToken(oldRing, http.MethodGet, "/api", "", now)

	newRing := mustTestRing(t, testSecretNew, testSecretOld)
	handler := mustTestServiceHandler(t, newRing, func() time.Time { return now })

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHMACKeyRing_RejectUnknownSecret(t *testing.T) {
	now := time.Now()

	unknownRing := mustTestRing(t, testSecretUnk, "")
	token := GenerateServiceToken(unknownRing, http.MethodGet, "/api", "", now)

	ring := mustTestRing(t, testSecretNew, testSecretOld)
	handler := mustTestServiceHandlerFatal(t, ring, func() time.Time { return now })

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHMACKeyRing_SingleSecretMode(t *testing.T) {
	now := time.Now()

	ring := mustTestRing(t, testSecretOne, "")
	token := GenerateServiceToken(ring, http.MethodGet, "/api", "", now)
	handler := mustTestServiceHandler(t, ring, func() time.Time { return now })

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHMACKeyRing_SameSecretBothPositions(t *testing.T) {
	now := time.Now()

	ring := mustTestRing(t, testSecretSam, testSecretSam)
	token := GenerateServiceToken(ring, http.MethodGet, "/api", "", now)
	handler := mustTestServiceHandler(t, ring, func() time.Time { return now })

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

func TestNewHMACKeyRing_ShortCurrentFails(t *testing.T) {
	_, err := NewHMACKeyRing([]byte("too-short"), nil)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "minimum is 32")
}

func TestNewHMACKeyRing_ShortPreviousFails(t *testing.T) {
	_, err := NewHMACKeyRing([]byte(testSecret), []byte("too-short"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "previous HMAC secret")
}

func TestGenerateServiceToken_NilRing(t *testing.T) {
	token := GenerateServiceToken(nil, "GET", "/api", "", time.Now())
	assert.Empty(t, token)
}

func TestHMACKeyRing_Secrets_SingleKey(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	secrets := ring.Secrets()
	assert.Len(t, secrets, 1)
	assert.Equal(t, []byte(testSecret), secrets[0])
}

func TestHMACKeyRing_Secrets_DualKey(t *testing.T) {
	ring := mustTestRing(t, testSecretNew, testSecretOld)
	secrets := ring.Secrets()
	assert.Len(t, secrets, 2)
	assert.Equal(t, []byte(testSecretNew), secrets[0])
	assert.Equal(t, []byte(testSecretOld), secrets[1])
}

func TestLoadHMACKeyRingFromEnv_CurrentOnly(t *testing.T) {
	t.Setenv(EnvServiceSecret, testSecret)
	t.Setenv(EnvServiceSecretPrevious, "")

	ring, err := LoadHMACKeyRingFromEnv()
	require.NoError(t, err)
	assert.Equal(t, []byte(testSecret), ring.Current())
	assert.Len(t, ring.Secrets(), 1)
}

func TestLoadHMACKeyRingFromEnv_CurrentAndPrevious(t *testing.T) {
	t.Setenv(EnvServiceSecret, testSecretNew)
	t.Setenv(EnvServiceSecretPrevious, testSecretOld)

	ring, err := LoadHMACKeyRingFromEnv()
	require.NoError(t, err)
	assert.Equal(t, []byte(testSecretNew), ring.Current())
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

func TestServiceTokenMiddleware_ValidToken(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	now := time.Now()
	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/health", "", now)
	handler := mustTestServiceHandler(t, ring, func() time.Time { return now })

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestServiceTokenMiddleware_InvalidToken(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	now := time.Now()
	handler := mustTestServiceHandlerFatal(t, ring, func() time.Time { return now })

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken 12345:deadbeef")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_MissingToken(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	handler := mustTestServiceHandlerFatal(t, ring, time.Now)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_WrongScheme(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	handler := mustTestServiceHandlerFatal(t, ring, time.Now)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer some-jwt")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_DifferentPath(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	now := time.Now()

	token := GenerateServiceToken(ring, http.MethodGet, "/other", "", now)
	handler := mustTestServiceHandlerFatal(t, ring, func() time.Time { return now })

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
	ring := mustTestRing(t, testSecret, "")
	now := time.Now()
	handler := mustTestServiceHandlerFatal(t, ring, func() time.Time { return now })

	oldTime := now.Add(-6 * time.Minute)
	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/health", "", oldTime)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_ExactBoundary_Rejected(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	now := time.Now()
	handler := mustTestServiceHandlerFatal(t, ring, func() time.Time { return now })

	boundaryTime := now.Add(-ServiceTokenMaxAge)
	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/health", "", boundaryTime)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_JustWithinWindow(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	now := time.Now()
	handler := mustTestServiceHandler(t, ring, func() time.Time { return now })

	recentTime := now.Add(-4*time.Minute - 59*time.Second)
	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/health", "", recentTime)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestServiceTokenMiddleware_FutureTimestamp_Rejected(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	now := time.Now()
	handler := mustTestServiceHandlerFatal(t, ring, func() time.Time { return now })

	futureTime := now.Add(6 * time.Minute)
	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/health", "", futureTime)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_InvalidFormat_NoColon(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
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
	// GenerateServiceToken now includes a random nonce, so two calls with the
	// same parameters produce different tokens. We verify structure instead.
	ring := mustTestRing(t, testSecret, "")
	ts := time.Unix(1700000000, 0)

	t1 := GenerateServiceToken(ring, http.MethodPost, "/api", "", ts)
	t2 := GenerateServiceToken(ring, http.MethodPost, "/api", "", ts)

	parts1 := strings.SplitN(t1, ":", 3)
	parts2 := strings.SplitN(t2, ":", 3)
	require.Len(t, parts1, 3, "token must have 3 colon-separated parts")
	require.Len(t, parts2, 3, "token must have 3 colon-separated parts")

	// Nonces differ between calls.
	assert.NotEqual(t, parts1[1], parts2[1], "nonces must differ between calls")

	// Different method produces a different HMAC (nonces also differ).
	t3 := GenerateServiceToken(ring, http.MethodGet, "/api", "", ts)
	assert.NotEqual(t, t1, t3)
}

func TestGenerateServiceToken_IncludesNonce(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	ts := time.Unix(1700000000, 0)
	token := GenerateServiceToken(ring, http.MethodGet, "/api", "", ts)

	parts := strings.SplitN(token, ":", 3)
	require.Len(t, parts, 3, "token must have 3 colon-separated parts")
	assert.NotEmpty(t, parts[1], "nonce must not be empty")
	assert.Len(t, parts[1], 32, "nonce must be 32 hex chars (16 bytes)")
}

func TestGenerateServiceToken_NonceUniqueness(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	ts := time.Unix(1700000000, 0)

	t1 := GenerateServiceToken(ring, http.MethodGet, "/api", "", ts)
	t2 := GenerateServiceToken(ring, http.MethodGet, "/api", "", ts)

	parts1 := strings.SplitN(t1, ":", 3)
	parts2 := strings.SplitN(t2, ":", 3)
	require.Len(t, parts1, 3)
	require.Len(t, parts2, 3)

	assert.NotEqual(t, parts1[1], parts2[1], "consecutive calls must produce different nonces")
	assert.NotEqual(t, t1, t2, "tokens with different nonces must differ")
}

func TestServiceTokenMiddleware_WithNonceStore_ReplayRejected(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	now := time.Now()
	store, err := NewInMemoryNonceStore(5 * time.Minute)
	require.NoError(t, err)
	handler := ServiceTokenMiddleware(ring,
		WithServiceTokenClock(func() time.Time { return now }),
		WithNonceStore(store),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	token := GenerateServiceToken(ring, http.MethodGet, "/api/v1/resource", "", now)

	// First use — accepted.
	req1 := httptest.NewRequest(http.MethodGet, "/api/v1/resource", nil)
	req1.Header.Set("Authorization", "ServiceToken "+token)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	assert.Equal(t, http.StatusOK, rec1.Code)

	// Replay — rejected.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/resource", nil)
	req2.Header.Set("Authorization", "ServiceToken "+token)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusUnauthorized, rec2.Code)
}

func TestServiceTokenMiddleware_WithNonceStore_UniqueTokensAccepted(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	now := time.Now()
	store, err := NewInMemoryNonceStore(5 * time.Minute)
	require.NoError(t, err)
	handler := ServiceTokenMiddleware(ring,
		WithServiceTokenClock(func() time.Time { return now }),
		WithNonceStore(store),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	token1 := GenerateServiceToken(ring, http.MethodGet, "/api/v1/resource", "", now)
	token2 := GenerateServiceToken(ring, http.MethodGet, "/api/v1/resource", "", now)

	req1 := httptest.NewRequest(http.MethodGet, "/api/v1/resource", nil)
	req1.Header.Set("Authorization", "ServiceToken "+token1)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	assert.Equal(t, http.StatusOK, rec1.Code, "first unique token must be accepted")

	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/resource", nil)
	req2.Header.Set("Authorization", "ServiceToken "+token2)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusOK, rec2.Code, "second unique token must be accepted")
}

func TestServiceTokenMiddleware_WithoutNonceStore_ReplayAllowed(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	now := time.Now()
	// No nonce store — backward-compat mode.
	handler := mustTestServiceHandler(t, ring, func() time.Time { return now })

	token := GenerateServiceToken(ring, http.MethodGet, "/api/v1/resource", "", now)

	req1 := httptest.NewRequest(http.MethodGet, "/api/v1/resource", nil)
	req1.Header.Set("Authorization", "ServiceToken "+token)
	rec1 := httptest.NewRecorder()
	handler.ServeHTTP(rec1, req1)
	assert.Equal(t, http.StatusOK, rec1.Code)

	// Same token again — still accepted because no nonce store.
	req2 := httptest.NewRequest(http.MethodGet, "/api/v1/resource", nil)
	req2.Header.Set("Authorization", "ServiceToken "+token)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusOK, rec2.Code, "replay must be allowed without a NonceStore")
}

func TestServiceTokenMiddleware_LegacyTwoPartFormat_Rejected(t *testing.T) {
	// 2-part tokens (format: {ts}:{hmac}) must be rejected with 401.
	// Per CLAUDE.md "not considering backward compatibility", the legacy
	// format introduced before PR#159 added nonce is no longer accepted.
	ring := mustTestRing(t, testSecret, "")
	now := time.Unix(1700000000, 0)

	// Craft a 2-part token with a valid-looking hex MAC segment.
	legacyToken := "1700000000:aabbccdd1122334455667788990011223344556677889900112233445566778899"

	handler := ServiceTokenMiddleware(ring,
		WithServiceTokenClock(func() time.Time { return now }),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called: 2-part token must be rejected")
	}))

	req := httptest.NewRequest(http.MethodGet, "/legacy/path", nil)
	req.Header.Set("Authorization", "ServiceToken "+legacyToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, "2-part legacy token must be rejected")
}

func TestServiceTokenMiddleware_WithMetrics_NoPanic(t *testing.T) {
	am, err := NewAuthMetrics(metrics.NopProvider{})
	require.NoError(t, err)

	ring := mustTestRing(t, testSecret, "")
	now := time.Now()
	token := GenerateServiceToken(ring, http.MethodGet, "/api", "", now)

	handler := ServiceTokenMiddleware(ring,
		WithServiceTokenClock(func() time.Time { return now }),
		WithServiceTokenMetrics(am),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// Success path.
	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Failure path (missing token).
	req2 := httptest.NewRequest(http.MethodGet, "/api", nil)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusUnauthorized, rec2.Code)
}

func TestServiceTokenMiddleware_QueryBoundInSignature(t *testing.T) {
	ring := mustTestRing(t, testSecret, "")
	now := time.Now()

	// Sign with query=foo=bar
	token := GenerateServiceToken(ring, http.MethodGet, "/api", "foo=bar", now)
	handler := mustTestServiceHandler(t, ring, func() time.Time { return now })

	// Same path+query should succeed.
	req := httptest.NewRequest(http.MethodGet, "/api?foo=bar", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Different query should fail.
	req2 := httptest.NewRequest(http.MethodGet, "/api?foo=baz", nil)
	req2.Header.Set("Authorization", "ServiceToken "+token)
	rec2 := httptest.NewRecorder()
	handler.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusUnauthorized, rec2.Code)
}

func TestCanonicalQuery_SortsKeys(t *testing.T) {
	assert.Equal(t, "a=1&b=2", canonicalQuery("b=2&a=1"))
	assert.Equal(t, "", canonicalQuery(""))
}
