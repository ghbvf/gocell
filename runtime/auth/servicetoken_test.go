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

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/clock/clockmock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
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

func mustTestServiceHandler(t *testing.T, ring *HMACKeyRing, clk clock.Clock) http.Handler {
	t.Helper()
	return ServiceTokenMiddleware(ring, clk,
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)),
	)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	)
}

func mustTestServiceHandlerFatal(t *testing.T, ring *HMACKeyRing, clk clock.Clock) http.Handler {
	t.Helper()
	return ServiceTokenMiddleware(ring, clk,
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
	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/api", "", now)

	singleRing := mustTestRing(t, testHMACKeyNew, "")
	handler := mustTestServiceHandler(t, singleRing, clockmock.New(now))

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHMACKeyRing_VerifyWithPrevious(t *testing.T) {
	now := time.Now()

	oldRing := mustTestRing(t, testHMACKeyOld, "")
	token := GenerateServiceToken(oldRing, "gocell", http.MethodGet, "/api", "", now)

	newRing := mustTestRing(t, testHMACKeyNew, testHMACKeyOld)
	handler := mustTestServiceHandler(t, newRing, clockmock.New(now))

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHMACKeyRing_RejectUnknownSecret(t *testing.T) {
	now := time.Now()

	unknownRing := mustTestRing(t, testHMACKeyUnk, "")
	token := GenerateServiceToken(unknownRing, "gocell", http.MethodGet, "/api", "", now)

	ring := mustTestRing(t, testHMACKeyNew, testHMACKeyOld)
	handler := mustTestServiceHandlerFatal(t, ring, clockmock.New(now))

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestHMACKeyRing_SingleSecretMode(t *testing.T) {
	now := time.Now()

	ring := mustTestRing(t, testHMACKeyOne, "")
	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/api", "", now)
	handler := mustTestServiceHandler(t, ring, clockmock.New(now))

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestHMACKeyRing_SameSecretBothPositions(t *testing.T) {
	now := time.Now()

	ring := mustTestRing(t, testHMACKeySam, testHMACKeySam)
	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/api", "", now)
	handler := mustTestServiceHandler(t, ring, clockmock.New(now))

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
	token := GenerateServiceToken(nil, "gocell", "GET", "/api", "", time.Now())
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
	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/internal/v1/health", "", now)
	handler := mustTestServiceHandler(t, ring, clockmock.New(now))

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestServiceTokenMiddleware_InvalidToken(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	handler := mustTestServiceHandlerFatal(t, ring, clockmock.New(now))

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken 12345:deadbeef")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_MissingToken(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	handler := mustTestServiceHandlerFatal(t, ring, clock.Real())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_WrongScheme(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	handler := mustTestServiceHandlerFatal(t, ring, clock.Real())

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer some-jwt")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_DifferentPath(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()

	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/other", "", now)
	handler := mustTestServiceHandlerFatal(t, ring, clockmock.New(now))

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_NilRing(t *testing.T) {
	handler := ServiceTokenMiddleware(nil, clock.Real())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called")
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
}

func TestServiceTokenMiddleware_TypedNilRing(t *testing.T) {
	var ring *HMACKeyRing
	handler := ServiceTokenMiddleware(ring, clock.Real())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	handler := ServiceTokenMiddleware(&shortKeyringStub{}, clock.Real())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
	handler := mustTestServiceHandlerFatal(t, ring, clockmock.New(now))

	oldTime := now.Add(svcTokenDNeg6min)
	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/internal/v1/health", "", oldTime)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_ExactBoundary_Rejected(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	handler := mustTestServiceHandlerFatal(t, ring, clockmock.New(now))

	boundaryTime := now.Add(-ServiceTokenMaxAge)
	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/internal/v1/health", "", boundaryTime)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_JustWithinWindow(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	handler := mustTestServiceHandler(t, ring, clockmock.New(now))

	recentTime := now.Add(svcTokenDNeg4min59s)
	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/internal/v1/health", "", recentTime)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestServiceTokenMiddleware_FutureTimestamp_Rejected(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	handler := mustTestServiceHandlerFatal(t, ring, clockmock.New(now))

	futureTime := now.Add(svcTokenD6min)
	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/internal/v1/health", "", futureTime)
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/health", nil)
	req.Header.Set("Authorization", "ServiceToken "+token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestServiceTokenMiddleware_InvalidFormat_NoColon(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	handler := ServiceTokenMiddleware(ring, clock.Real(),
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
	// Spec: GenerateServiceToken 4-part format {ts}:{nonce}:{caller_cell}:{hex_hmac}.
	// Two calls with the same parameters produce different tokens (random nonce).
	ring := mustTestRing(t, testHMACKey, "")
	ts := time.Unix(1700000000, 0)

	t1 := GenerateServiceToken(ring, "accesscore", http.MethodPost, "/api", "", ts)
	t2 := GenerateServiceToken(ring, "accesscore", http.MethodPost, "/api", "", ts)

	parts1 := strings.SplitN(t1, ":", 4)
	parts2 := strings.SplitN(t2, ":", 4)
	require.Len(t, parts1, 4, "token must have 4 colon-separated parts: {ts}:{nonce}:{caller_cell}:{hex_hmac}")
	require.Len(t, parts2, 4, "token must have 4 colon-separated parts: {ts}:{nonce}:{caller_cell}:{hex_hmac}")

	// caller_cell segment (parts[2]) must be the provided callerCell value.
	assert.Equal(t, "accesscore", parts1[2], "caller_cell segment must match callerCell arg")

	// Nonces differ between calls.
	assert.NotEqual(t, parts1[1], parts2[1], "nonces must differ between calls")

	// Different method produces a different HMAC (nonces also differ).
	t3 := GenerateServiceToken(ring, "accesscore", http.MethodGet, "/api", "", ts)
	assert.NotEqual(t, t1, t3)
}

func TestGenerateServiceToken_IncludesNonce(t *testing.T) {
	// Spec: 4-part format {ts}:{nonce}:{caller_cell}:{hex_hmac}
	ring := mustTestRing(t, testHMACKey, "")
	ts := time.Unix(1700000000, 0)
	token := GenerateServiceToken(ring, "configcore", http.MethodGet, "/api", "", ts)

	parts := strings.SplitN(token, ":", 4)
	require.Len(t, parts, 4, "token must have 4 colon-separated parts: {ts}:{nonce}:{caller_cell}:{hex_hmac}")
	assert.NotEmpty(t, parts[1], "nonce must not be empty")
	assert.Len(t, parts[1], 32, "nonce must be 32 hex chars (16 bytes)")
	assert.Equal(t, "configcore", parts[2], "caller_cell must appear in part[2]")
}

func TestGenerateServiceToken_NonceUniqueness(t *testing.T) {
	// Spec: 4-part format, consecutive calls produce different nonces.
	ring := mustTestRing(t, testHMACKey, "")
	ts := time.Unix(1700000000, 0)

	t1 := GenerateServiceToken(ring, "auditcore", http.MethodGet, "/api", "", ts)
	t2 := GenerateServiceToken(ring, "auditcore", http.MethodGet, "/api", "", ts)

	parts1 := strings.SplitN(t1, ":", 4)
	parts2 := strings.SplitN(t2, ":", 4)
	require.Len(t, parts1, 4)
	require.Len(t, parts2, 4)

	assert.NotEqual(t, parts1[1], parts2[1], "consecutive calls must produce different nonces")
	assert.NotEqual(t, t1, t2, "tokens with different nonces must differ")
}

func TestServiceTokenMiddleware_WithNonceStore_ReplayRejected(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	store, err := NewInMemoryNonceStore(ServiceTokenNonceTTL, clock.Real())
	require.NoError(t, err)
	handler := ServiceTokenMiddleware(ring, clockmock.New(now),
		WithServiceTokenNonceStore(store),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/api/v1/resource", "", now)

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
	store, err := NewInMemoryNonceStore(ServiceTokenNonceTTL, clock.Real())
	require.NoError(t, err)
	handler := ServiceTokenMiddleware(ring, clockmock.New(now),
		WithServiceTokenNonceStore(store),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	token1 := GenerateServiceToken(ring, "gocell", http.MethodGet, "/api/v1/resource", "", now)
	token2 := GenerateServiceToken(ring, "gocell", http.MethodGet, "/api/v1/resource", "", now)

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
	handler := ServiceTokenMiddleware(ring, clockmock.New(now))(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be called when NonceStore is missing")
	}))

	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/internal/v1/resource", "", now)
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
	handler := ServiceTokenMiddleware(ring, clockmock.New(now),
		WithServiceTokenNonceStore(NewNoopNonceStore()),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("handler must not be called when NonceStore is Noop")
	}))

	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/internal/v1/resource", "", now)
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
	handler := ServiceTokenMiddleware(nil, clock.Real())(
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

	handler := ServiceTokenMiddleware(ring, clockmock.New(now),
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

	handler := ServiceTokenMiddleware(ring, clockmock.New(now),
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
	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/api", "", now)

	handler := ServiceTokenMiddleware(ring, clockmock.New(now),
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
	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/api", "foo=bar", now)
	handler := mustTestServiceHandler(t, ring, clockmock.New(now))

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
	token := GenerateServiceToken(ring, "gocell", http.MethodGet, "/internal/v1/resource", "", now)

	var gotPrincipal *Principal
	handler := ServiceTokenMiddleware(ring, clockmock.New(now),
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
	// Spec: Subject empty — identity via CallerCellID, not Subject.
	assert.Empty(t, gotPrincipal.Subject,
		"service principal Subject should be empty after CallerCellID migration")
	// Spec: Roles nil — caller-cell identity replaces role-based internal authz.
	assert.Nil(t, gotPrincipal.Roles,
		"service principal Roles should be nil after CallerCellID migration")
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

	handler := ServiceTokenMiddleware(ring, clockmock.New(now),
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

// --- Wave 1 RED tests: 4-part token + CallerCellID propagation ---

// TestServiceTokenMiddleware_CallerCellPropagated verifies that the 4-part token
// propagates CallerCellID into Principal after successful validation.
//
// Spec: GenerateServiceToken(ring, callerCell, method, path, query, ts) produces a
// 4-part token {ts}:{nonce}:{caller_cell}:{hex_hmac}; the middleware must extract
// caller_cell and set Principal.CallerCellID = callerCell.
func TestServiceTokenMiddleware_CallerCellPropagated(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()
	// Spec: 4-part signature with explicit callerCell
	token := GenerateServiceToken(ring, "accesscore", http.MethodGet, "/internal/v1/resource", "", now)

	var gotPrincipal *Principal
	handler := ServiceTokenMiddleware(ring, clockmock.New(now),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)),
	)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p, ok := FromContext(r.Context())
			require.True(t, ok, "Principal must be present after valid service token")
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
	// Spec: CallerCellID must equal the callerCell argument passed to GenerateServiceToken.
	assert.Equal(t, "accesscore", gotPrincipal.CallerCellID,
		"Principal.CallerCellID must be propagated from the 4-part token caller_cell segment")
}

// TestServiceTokenMiddleware_TamperedCallerCell_Rejected verifies that tampering
// with the caller_cell segment in the 4-part token causes MAC verification failure.
//
// Spec: the HMAC is computed over all 4 parts including caller_cell; replacing
// caller_cell with a different value after signing → MAC mismatch → 401.
func TestServiceTokenMiddleware_TamperedCallerCell_Rejected(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()

	// Sign with "accesscore" as caller_cell.
	goodToken := GenerateServiceToken(ring, "accesscore", http.MethodGet, "/internal/v1/resource", "", now)

	// Tamper: replace the caller_cell segment (parts[2]) with "configcore".
	parts := strings.SplitN(goodToken, ":", 4)
	require.Len(t, parts, 4, "GenerateServiceToken must produce a 4-part token")
	tamperedToken := parts[0] + ":" + parts[1] + ":configcore:" + parts[3]

	handler := ServiceTokenMiddleware(ring, clockmock.New(now),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)),
	)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("handler must not be called: tampered caller_cell must cause MAC failure")
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "ServiceToken "+tamperedToken)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Spec: tampered caller_cell → MAC mismatch → 401.
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"tampered caller_cell must be rejected with 401")
}

// TestServiceTokenMiddleware_CallerCellWithColon_Rejected verifies that a
// callerCell containing ':' is rejected at generation time (fail-closed).
//
// Spec: caller_cell is a path component separated by ':'; a value containing ':'
// would corrupt the 4-part structure and is forbidden.
func TestServiceTokenMiddleware_CallerCellWithColon_Rejected(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()

	// Spec: GenerateServiceToken must return "" when callerCell contains ':'
	token := GenerateServiceToken(ring, "bad:cell", http.MethodGet, "/internal/v1/resource", "", now)
	assert.Empty(t, token,
		"GenerateServiceToken must return empty string when callerCell contains ':'")
}

// TestServiceTokenMiddleware_EmptyCallerCell_Rejected verifies that an empty
// callerCell causes GenerateServiceToken to return "".
//
// Spec: callerCell is mandatory — an empty value means the caller forgot to set it.
func TestServiceTokenMiddleware_EmptyCallerCell_Rejected(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Now()

	// Spec: GenerateServiceToken must return "" when callerCell is empty.
	token := GenerateServiceToken(ring, "", http.MethodGet, "/internal/v1/resource", "", now)
	assert.Empty(t, token,
		"GenerateServiceToken must return empty string when callerCell is empty")
}

// TestClassifyServiceTokenVerifyError verifies that classifyServiceTokenVerifyError
// maps errors to the correct metric reason label for all classified cases.
func TestClassifyServiceTokenVerifyError(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantReason string
	}{
		{
			name:       "nil error — ok",
			err:        nil,
			wantReason: "ok",
		},
		{
			name:       "legacy 2-part format",
			err:        errcode.NewAuth(errcode.ErrAuthUnauthorized, "legacy 2-part service token format rejected"),
			wantReason: "legacy_format",
		},
		{
			name:       "legacy 3-part format",
			err:        errcode.NewAuth(errcode.ErrAuthUnauthorized, "legacy 3-part service token format rejected"),
			wantReason: "legacy_format",
		},
		{
			name:       "expired token",
			err:        errcode.NewAuth(errcode.ErrAuthTokenExpired, "service token expired"),
			wantReason: "expired",
		},
		{
			name:       "invalid MAC",
			err:        errcode.NewAuth(errcode.ErrAuthUnauthorized, "invalid service token MAC"),
			wantReason: "invalid_mac",
		},
		{
			name:       "missing caller cell",
			err:        errcode.NewAuth(errcode.ErrAuthUnauthorized, "caller cell missing"),
			wantReason: "missing_caller_cell",
		},
		{
			name:       "invalid caller cell — with actual value in message",
			err:        errcode.NewAuth(errcode.ErrAuthUnauthorized, `caller cell id "Bad-Cell" invalid (must match ^[a-z][a-z0-9-]*$)`),
			wantReason: "invalid_caller_cell",
		},
		{
			name:       "other invalid format",
			err:        errcode.NewAuth(errcode.ErrAuthUnauthorized, msgInvalidServiceTokenFormat),
			wantReason: "invalid_format",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyServiceTokenVerifyError(tc.err)
			assert.Equal(t, tc.wantReason, got)
		})
	}
}

// TestServiceToken_EmptyCallerCell_MetricLabel verifies that a 4-part token
// with an empty caller_cell segment is rejected with HTTP 401 AND records the
// metric label "missing_caller_cell".
func TestServiceToken_EmptyCallerCell_MetricLabel(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Unix(1700000000, 0)

	spy := newSpyProvider()
	am, err := NewAuthMetrics(spy)
	require.NoError(t, err)

	// Craft a 4-part token with an empty callerCell: ts:nonce::deadbeef...
	ts := fmt.Sprintf("%d", now.Unix())
	crafted := ts + ":somenonce16bytes::deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	handler := ServiceTokenMiddleware(ring, clockmock.New(now),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)),
		WithServiceTokenMetrics(am),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called: empty caller cell must be rejected")
	}))

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/test", nil)
	req.Header.Set("Authorization", "ServiceToken "+crafted)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	spy.assertServiceVerify(t, "failure", "missing_caller_cell")
}

// TestServiceToken_InvalidCallerCellPattern_MetricLabel verifies that a 4-part
// token with a caller_cell not matching ^[a-z][a-z0-9-]*$ is rejected with 401
// AND records the metric label "invalid_caller_cell".
func TestServiceToken_InvalidCallerCellPattern_MetricLabel(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Unix(1700000000, 0)

	spy := newSpyProvider()
	am, err := NewAuthMetrics(spy)
	require.NoError(t, err)

	// Craft a 4-part token with an invalid callerCell "Bad-Cell" (uppercase B).
	ts := fmt.Sprintf("%d", now.Unix())
	crafted := ts + ":somenonce16bytes:Bad-Cell:deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	handler := ServiceTokenMiddleware(ring, clockmock.New(now),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)),
		WithServiceTokenMetrics(am),
	)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("should not be called: invalid caller cell must be rejected")
	}))

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/test", nil)
	req.Header.Set("Authorization", "ServiceToken "+crafted)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	spy.assertServiceVerify(t, "failure", "invalid_caller_cell")
}

// TestServiceTokenMiddleware_LegacyThreePart_Rejected verifies that the old
// 3-part token format {ts}:{nonce}:{hex_hmac} (without caller_cell) is rejected.
//
// Spec: the new 4-part format is {ts}:{nonce}:{caller_cell}:{hex_hmac}; a
// 3-part token no longer has a caller_cell and must be rejected as legacy.
func TestServiceTokenMiddleware_LegacyThreePart_Rejected(t *testing.T) {
	ring := mustTestRing(t, testHMACKey, "")
	now := time.Unix(1700000000, 0)

	// Construct a 3-part token manually (pre-4-part format, without callerCell).
	legacyToken := legacyTwoPartToken(testHMACKey, http.MethodGet, "/internal/v1/resource", now)
	// legacyTwoPartToken produces "{ts}:{hex_hmac}" (2-part), so we simulate the
	// old 3-part by inserting a nonce: "{ts}:{nonce}:{hex_hmac}".
	oldThreePart := strings.SplitN(legacyToken, ":", 2)
	require.Len(t, oldThreePart, 2)
	simulatedThreePart := oldThreePart[0] + ":aaabbbccc000111222333" + ":" + oldThreePart[1]

	handler := ServiceTokenMiddleware(ring, clockmock.New(now),
		WithServiceTokenNonceStore(mustNewInMemoryNonceStore(t)),
	)(
		http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			t.Fatal("handler must not be called: 3-part legacy token must be rejected")
		}),
	)

	req := httptest.NewRequest(http.MethodGet, "/internal/v1/resource", nil)
	req.Header.Set("Authorization", "ServiceToken "+simulatedThreePart)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Spec: 3-part format (no caller_cell) must be rejected.
	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"3-part legacy token (no caller_cell) must be rejected with 401")
}
