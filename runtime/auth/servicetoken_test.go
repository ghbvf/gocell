package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// test HMAC keys — each exactly 33 bytes (>= MinHMACKeyBytes=32).
const (
	testHMACKey    = "test-hmackey-padding-to-32bytes!!" // len=33
	testHMACKeyOld = "old--hmackey-padding-to-32bytes!!" // len=33
	testHMACKeyNew = "new--hmackey-padding-to-32bytes!!" // len=33
	testHMACKeyUnk = "unkn-hmackey-padding-to-32bytes!!" // len=33
	testHMACKeyOne = "only-hmackey-padding-to-32bytes!!" // len=33
	testHMACKeySam = "same-hmackey-padding-to-32bytes!!" // len=33
)

const (
	svcTokenDNeg6min    = -6 * time.Minute
	svcTokenD6min       = 6 * time.Minute
	svcTokenDNeg4min59s = -4*time.Minute - 59*time.Second
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
	return ServiceTokenMiddleware(ring,
		WithServiceTokenClock(clockFn),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)),
	)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)
}

func mustTestServiceHandlerFatal(t *testing.T, ring *HMACKeyRing, clockFn func() time.Time) http.Handler {
	t.Helper()
	return ServiceTokenMiddleware(ring,
		WithServiceTokenClock(clockFn),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)),
	)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("should not be called")
		}),
	)
}

func TestHMACKeyRing_Current_ReturnsCopy(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	c := ring.Current()
	original := make([]byte, len(c))
	copy(original, c)

	c[0] = 0xFF

	assert.Equal(t, original, ring.Current(), "Current() must return a defensive copy")
}

func TestHMACKeyRing_SignWithCurrent(t *testing.T) {
	ring := mustTestRing(t, testHMACKeyNew, testHMACKeyOld)
	now := time.Now()
	token := GenerateServiceToken(ring, http.MethodGet, "/api", "", now)

	singleRing := mustTestRing(t, testHMACKeyNew, "")
	handler := mustTestServiceHandler(t, singleRing, func() time.Time { return now })

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHMACKeyRing_VerifyWithPrevious(t *testing.T) {
	now := time.Now()

	oldRing := mustTestRing(t, testHMACKeyOld, "")
	token := GenerateServiceToken(oldRing, http.MethodGet, "/api", "", now)

	newRing := mustTestRing(t, testHMACKeyNew, testHMACKeyOld)
	handler := mustTestServiceHandler(t, newRing, func() time.Time { return now })

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHMACKeyRing_RejectUnknownSecret(t *testing.T) {
	now := time.Now()

	unknownRing := mustTestRing(t, testHMACKeyUnk, "")
	token := GenerateServiceToken(unknownRing, http.MethodGet, "/api", "", now)

	ring := mustTestRing(t, testHMACKeyNew, testHMACKeyOld)
	handler := mustTestServiceHandlerFatal(t, ring, func() time.Time { return now })

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHMACKeyRing_SingleSecretMode(t *testing.T) {
	now := time.Now()

	ring := mustTestRing(t, testHMACKeyOne, "")
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

	ring := mustTestRing(t, testHMACKeySam, testHMACKeySam)
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
	_, err := NewHMACKeyRing([]byte(testHMACKey), []byte("too-short"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "previous HMAC secret")
}

func TestGenerateServiceToken_NilRing(t *testing.T) {
	token := GenerateServiceToken(nil, "GET", "/api", "", time.Now())
	assert.Empty(t, token)
}

func TestHMACKeyRing_Secrets_SingleKey(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	secrets := ring.Secrets()
	assert.Len(t, secrets, 1)
	assert.Equal(t, []byte(testHMACKey), secrets[0])
}

func TestHMACKeyRing_Secrets_DualKey(t *testing.T) {
	ring := mustTestRing(t, testHMACKeyNew, testHMACKeyOld)
	secrets := ring.Secrets()
	assert.Len(t, secrets, 2)
	assert.Equal(t, []byte(testHMACKeyNew), secrets[0])
	assert.Equal(t, []byte(testHMACKeyOld), secrets[1])
}

func TestLoadHMACKeyRingFromEnv_CurrentOnly(t *testing.T) {
	t.Setenv(EnvServiceSecret, testHMACKey)
	t.Setenv(EnvServiceSecretPrevious, "")

	ring, err := LoadHMACKeyRingFromEnv()
	require.NoError(t, err)
	assert.Equal(t, []byte(testHMACKey), ring.Current())
	assert.Len(t, ring.Secrets(), 1)
}

func TestLoadHMACKeyRingFromEnv_CurrentAndPrevious(t *testing.T) {
	t.Setenv(EnvServiceSecret, testHMACKeyNew)
	t.Setenv(EnvServiceSecretPrevious, testHMACKeyOld)

	ring, err := LoadHMACKeyRingFromEnv()
	require.NoError(t, err)
	assert.Equal(t, []byte(testHMACKeyNew), ring.Current())
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
	ring := mustTestRing(t, testHMACKey, "")
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
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	handler := mustTestServiceHandlerFatal(t, ring, func() time.Time { return now })

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken 12345:deadbeef")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_MissingToken(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	handler := mustTestServiceHandlerFatal(t, ring, time.Now)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_WrongScheme(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	handler := mustTestServiceHandlerFatal(t, ring, time.Now)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer some-jwt")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_DifferentPath(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
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

func TestServiceTokenMiddleware_TypedNilRing(t *testing.T) {
	var ring *HMACKeyRing
	handler := ServiceTokenMiddleware(ring)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

// shortKeyringStub returns a sub-MinHMACKeyBytes secret to exercise the
// defense-in-depth strength check inside ServiceTokenMiddleware (PR269 round-3
// F5). cell.NewAuthServiceToken would normally reject this at construction
// time; this test bypasses that path by calling ServiceTokenMiddleware directly,
// which is the threat model the wiring-time check defends against.
type shortKeyringStub struct{}

func (*shortKeyringStub) Current() []byte   { return []byte("short") } // 5 bytes
func (*shortKeyringStub) Secrets() [][]byte { return [][]byte{(&shortKeyringStub{}).Current()} }

func TestServiceTokenMiddleware_ShortKeyReturnsErrorMiddleware(t *testing.T) {
	handler := ServiceTokenMiddleware(&shortKeyringStub{})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be reached when ring secret is below MinHMACKeyBytes")
	}))

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code,
		"sub-strength HMAC ring must yield 500 from the wiring-time guard")
}

func TestServiceTokenMiddleware_ExpiredTimestamp(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	handler := mustTestServiceHandlerFatal(t, ring, func() time.Time { return now })

	oldTime := now.Add(svcTokenDNeg6min)
	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/health", "", oldTime)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_ExactBoundary_Rejected(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
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
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	handler := mustTestServiceHandler(t, ring, func() time.Time { return now })

	recentTime := now.Add(svcTokenDNeg4min59s)
	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/health", "", recentTime)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestServiceTokenMiddleware_FutureTimestamp_Rejected(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	handler := mustTestServiceHandlerFatal(t, ring, func() time.Time { return now })

	futureTime := now.Add(svcTokenD6min)
	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/health", "", futureTime)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_InvalidFormat_NoColon(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	handler := ServiceTokenMiddleware(ring,
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	ring := mustTestRing(t, testHMACKey, "")
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
	ring := mustTestRing(t, testHMACKey, "")
	ts := time.Unix(1700000000, 0)
	token := GenerateServiceToken(ring, http.MethodGet, "/api", "", ts)

	parts := strings.SplitN(token, ":", 3)
	require.Len(t, parts, 3, "token must have 3 colon-separated parts")
	assert.NotEmpty(t, parts[1], "nonce must not be empty")
	assert.Len(t, parts[1], 32, "nonce must be 32 hex chars (16 bytes)")
}

func TestGenerateServiceToken_NonceUniqueness(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
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
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	store, err := NewInMemoryNonceStore(ServiceTokenNonceTTL)
	require.NoError(t, err)
	handler := ServiceTokenMiddleware(ring,
		WithServiceTokenClock(func() time.Time { return now }),
		WithServiceTokenNonceStore(store),
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
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	store, err := NewInMemoryNonceStore(ServiceTokenNonceTTL)
	require.NoError(t, err)
	handler := ServiceTokenMiddleware(ring,
		WithServiceTokenClock(func() time.Time { return now }),
		WithServiceTokenNonceStore(store),
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

// TestServiceTokenMiddleware_DefaultNoNonceStore_ReturnsErrorMiddleware verifies
// that ServiceTokenMiddleware without WithServiceTokenNonceStore returns an error
// middleware that serves 500 on every request. This aligns with the fail-closed
// construction guard in NewServiceTokenAuthenticator.
func TestServiceTokenMiddleware_DefaultNoNonceStore_ReturnsErrorMiddleware(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	// No WithServiceTokenNonceStore — must return an error middleware, not a valid handler.
	handler := ServiceTokenMiddleware(ring,
		WithServiceTokenClock(func() time.Time { return now }),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be called when NonceStore is missing")
	}))

	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/resource", "", now)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code,
		"missing NonceStore must produce 500 error middleware (fail-closed)")
}

// TestServiceTokenMiddleware_NoopNonceStoreSupplied_ReturnsErrorMiddleware verifies
// that explicitly passing a NoopNonceStore via WithServiceTokenNonceStore also
// produces an error middleware. NoopNonceStore is a marker type used by upper layers
// to detect misconfig; it must never reach live requests.
func TestServiceTokenMiddleware_NoopNonceStoreSupplied_ReturnsErrorMiddleware(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	handler := ServiceTokenMiddleware(ring,
		WithServiceTokenClock(func() time.Time { return now }),
		WithServiceTokenNonceStore(NewNoopNonceStore()),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be called when NonceStore is Noop")
	}))

	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/resource", "", now)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code,
		"NoopNonceStore must produce 500 error middleware (fail-closed)")
}

// TestServiceTokenMiddleware_NilRing_UsesSharedHelper verifies that the nil-ring
// error path (pre-existing) and the new NonceStore-missing/Noop paths all return
// the same HTTP 500 status, confirming they share the errorMiddlewareInternal helper.
func TestServiceTokenMiddleware_NilRing_UsesSharedHelper(t *testing.T) {
	// nil ring — pre-existing path, should still produce 500.
	handler := ServiceTokenMiddleware(nil)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("handler must not be called for nil ring")
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code,
		"nil ring path must yield 500 via shared errorMiddlewareInternal helper")
}

// legacyTwoPartToken computes what a pre-PR#159 signer would have emitted:
// HMAC-SHA256(secret, "METHOD PATH TIMESTAMP") → hex, prefixed with "{ts}:".
// This is the exact token the old 2-part format would produce.
func legacyTwoPartToken(secret, method, path string, ts time.Time) string {
	tsStr := strconv.FormatInt(ts.Unix(), 10)
	message := fmt.Sprintf("%s %s %s", method, path, tsStr)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(message))
	return tsStr + ":" + hex.EncodeToString(mac.Sum(nil))
}

// TestServiceTokenMiddleware_LegacyTwoPartFormat_RealSignature_Rejected verifies
// that a semantically valid 2-part token (real HMAC, correct secret, fresh
// timestamp) is rejected with 401 ERR_AUTH_UNAUTHORIZED.
//
// Semantic boundary: if the removed 2-part compat branch (PR#159) were
// reintroduced, the legacy HMAC would match and the middleware would return 200
// — this test would then FAIL, proving it truly locks the boundary.
func TestServiceTokenMiddleware_LegacyTwoPartFormat_RealSignature_Rejected(t *testing.T) {
	const (
		method = http.MethodGet
		path   = "/legacy/path"
	)
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Unix(1700000000, 0)

	// Recompute the exact token a pre-PR#159 signer would have emitted:
	// HMAC-SHA256(testHMACKey, "GET /legacy/path 1700000000") → hex.
	// The 2-part format has no nonce, so this is a fully valid legacy credential.
	legacyToken := legacyTwoPartToken(testHMACKey, method, path, now)

	handler := ServiceTokenMiddleware(ring,
		WithServiceTokenClock(func() time.Time { return now }),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called: semantically valid 2-part token must be rejected")
	}))

	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", "ServiceToken "+legacyToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"2-part legacy token with real HMAC must be rejected")
	assertErrorCode(t, rec, "ERR_AUTH_UNAUTHORIZED")
}

// TestServiceTokenMiddleware_MalformedToken_TwoSegments_Rejected verifies that a
// 2-part token with a forged (non-HMAC) MAC segment is rejected with 401.
// This test covers structural rejection, while
// TestServiceTokenMiddleware_LegacyTwoPartFormat_RealSignature_Rejected covers
// the semantic boundary (valid HMAC, wrong format).
func TestServiceTokenMiddleware_MalformedToken_TwoSegments_Rejected(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Unix(1700000000, 0)

	// Forged hex MAC — not a real HMAC output.
	forgedMAC := "1700000000:aabbccdd1122334455667788990011223344556677889900112233445566778899"

	handler := ServiceTokenMiddleware(ring,
		WithServiceTokenClock(func() time.Time { return now }),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called: 2-part token must be rejected")
	}))

	req := httptest.NewRequest(http.MethodGet, "/legacy/path", nil)
	req.Header.Set("Authorization", "ServiceToken "+forgedMAC)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code, "2-part forged token must be rejected")
	assertErrorCode(t, rec, "ERR_AUTH_UNAUTHORIZED")
}

func TestServiceTokenMiddleware_WithMetrics_NoPanic(t *testing.T) {
	am, err := NewAuthMetrics(metrics.NopProvider{})
	require.NoError(t, err)

	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	token := GenerateServiceToken(ring, http.MethodGet, "/api", "", now)

	handler := ServiceTokenMiddleware(ring,
		WithServiceTokenClock(func() time.Time { return now }),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)),
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
	ring := mustTestRing(t, testHMACKey, "")
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

// --- T5: ServicePrincipal injection tests ---

// TestServiceTokenMiddleware_InjectsServicePrincipal verifies that a valid
// ServiceToken causes the middleware to inject a Principal into the request
// context with the correct service identity fields.
func TestServiceTokenMiddleware_InjectsServicePrincipal(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	token := GenerateServiceToken(ring, http.MethodGet, "/internal/v1/resource", "", now)

	var gotPrincipal *Principal
	handler := ServiceTokenMiddleware(ring,
		WithServiceTokenClock(func() time.Time { return now }),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)),
	)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := FromContext(r.Context())
			require.True(t, ok, "Principal must be present in context after valid service token")
			gotPrincipal = p
			w.WriteHeader(http.StatusOK)
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	require.NotNil(t, gotPrincipal)
	assert.Equal(t, PrincipalService, gotPrincipal.Kind)
	assert.Equal(t, ServiceNameInternal, gotPrincipal.Subject)
	assert.Contains(t, gotPrincipal.Roles, RoleInternalAdmin)
	assert.Equal(t, "service_token", gotPrincipal.AuthMethod)
	assert.False(t, gotPrincipal.PasswordResetRequired)
}

func TestCanonicalQuery_SortsKeys(t *testing.T) {
	assert.Equal(t, "a=1&b=2", canonicalQuery("b=2&a=1"))
	assert.Equal(t, "", canonicalQuery(""))
}

// spyCounterVec records each (result, reason) pair observed via Inc().
type spyCounterVec struct {
	labels   []string
	recorded []spyRecord
}

type spyRecord struct {
	result string
	reason string
}

func (v *spyCounterVec) Registered() bool { return true }
func (v *spyCounterVec) With(l metrics.Labels) metrics.Counter {
	metrics.MustValidateLabels(v.labels, l)
	return &spyCounter{vec: v, result: l["result"], reason: l["reason"]}
}

type spyCounter struct {
	vec    *spyCounterVec
	result string
	reason string
}

func (c *spyCounter) Inc() {
	c.vec.recorded = append(c.vec.recorded, spyRecord{result: c.result, reason: c.reason})
}
func (c *spyCounter) Add(_ float64) {}

// spyProvider is a metrics.Provider that returns a spyCounterVec for the
// service-token counter and no-ops for everything else.
type spyProvider struct {
	svcVec *spyCounterVec
}

func newSpyProvider() *spyProvider {
	return &spyProvider{
		svcVec: &spyCounterVec{labels: []string{"result", "reason"}},
	}
}

func (p *spyProvider) CounterVec(opts metrics.CounterOpts) (metrics.CounterVec, error) {
	if opts.Name == "auth_service_token_verify_total" {
		return p.svcVec, nil
	}
	return metrics.NopProvider{}.CounterVec(opts)
}

func (p *spyProvider) HistogramVec(opts metrics.HistogramOpts) (metrics.HistogramVec, error) {
	return metrics.NopProvider{}.HistogramVec(opts)
}

func (p *spyProvider) Unregister(_ metrics.Collector) error { return nil }

func (p *spyProvider) assertServiceVerify(t *testing.T, result, reason string) {
	t.Helper()
	for _, r := range p.svcVec.recorded {
		if r.result == result && r.reason == reason {
			return
		}
	}
	t.Errorf("expected service verify record {result=%q reason=%q}, got %v",
		result, reason, p.svcVec.recorded)
}

// TestServiceToken_LegacyTwoPart_MetricLabel verifies that a 2-part legacy token
// ({timestamp}:{hex_hmac}, no nonce) is rejected with HTTP 401 AND records the
// metric label "legacy_format" (not "invalid_format").
func TestServiceToken_LegacyTwoPart_MetricLabel(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Unix(1700000000, 0)

	spy := newSpyProvider()
	am, err := NewAuthMetrics(spy)
	require.NoError(t, err)

	legacyToken := legacyTwoPartToken(testHMACKey, http.MethodGet, "/internal/v1/test", now)

	handler := ServiceTokenMiddleware(ring,
		WithServiceTokenClock(func() time.Time { return now }),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)),
		WithServiceTokenMetrics(am),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called: 2-part token must be rejected")
	}))

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/test", nil)
	req.Header.Set("Authorization", "ServiceToken "+legacyToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assertErrorCode(t, rec, "ERR_AUTH_UNAUTHORIZED")
	spy.assertServiceVerify(t, "failure", "legacy_format")
}
