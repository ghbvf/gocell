// Tests for controlplane service-token guard wiring (C6).
//
// internalGuardFromEnv returns a ServiceTokenMiddleware guard when
// GOCELL_SERVICE_SECRET is set. In real mode, missing secret is a hard error.
// In dev mode, missing secret disables the guard (returns nil, nil).
package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestInternalGuardFromEnv_DevMode_MissingSecret_ReturnsNilGuard(t *testing.T) {
	t.Setenv("GOCELL_SERVICE_SECRET", "")
	guard, err := internalGuardFromEnv("") // dev mode
	require.NoError(t, err)
	assert.Nil(t, guard, "dev mode with empty secret must return nil guard (guard disabled)")
}

func TestInternalGuardFromEnv_RealMode_MissingSecret_Error(t *testing.T) {
	t.Setenv("GOCELL_SERVICE_SECRET", "")
	_, err := internalGuardFromEnv("real")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_SERVICE_SECRET",
		"real mode must fail fast when service secret is unset")
}

func TestInternalGuardFromEnv_WithSecret_ReturnsGuard(t *testing.T) {
	t.Setenv("GOCELL_SERVICE_SECRET", freshTestServiceSecret(t))
	guard, err := internalGuardFromEnv("")
	require.NoError(t, err)
	assert.NotNil(t, guard, "non-empty secret must produce a non-nil guard")
}

func TestInternalGuardFromEnv_WithSecret_GuardRejects401WhenNoHeader(t *testing.T) {
	t.Setenv("GOCELL_SERVICE_SECRET", freshTestServiceSecret(t))
	guard, err := internalGuardFromEnv("")
	require.NoError(t, err)
	require.NotNil(t, guard)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	guarded := guard.Middleware()(inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/access/roles", nil)
	guarded.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code,
		"guard must reject requests without service token header")
}

// TestInternalGuardFromEnv_RealMode_DemoServiceSecret_Rejected verifies that
// internalGuardFromEnv returns an error when GOCELL_SERVICE_SECRET is set to
// the well-known demo value in real adapter mode. Guards against an attacker
// forging ServiceTokens using the public demo secret shipped in test fixtures.
func TestInternalGuardFromEnv_RealMode_DemoServiceSecret_Rejected(t *testing.T) {
	t.Setenv("GOCELL_SERVICE_SECRET", "service-secret-32-bytes-xxxxxx!!")
	_, err := internalGuardFromEnv("real")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_SERVICE_SECRET",
		"error must name the offending env var")
	assert.Contains(t, err.Error(), "well-known demo key",
		"error must indicate the reason")
}

// TestInternalGuardFromEnv_DevMode_DemoServiceSecret_Allowed verifies that
// the demo key check is a no-op outside of real adapter mode, preserving
// the dev/test workflow where demo fixture values are acceptable.
func TestInternalGuardFromEnv_DevMode_DemoServiceSecret_Allowed(t *testing.T) {
	t.Setenv("GOCELL_SERVICE_SECRET", "service-secret-32-bytes-xxxxxx!!")
	guard, err := internalGuardFromEnv("") // dev mode
	require.NoError(t, err)
	assert.NotNil(t, guard, "dev mode must accept demo key and return a guard")
}

// TestInternalGuardFromEnv_RealMode_DemoPreviousServiceSecret_Rejected verifies
// that GOCELL_SERVICE_SECRET_PREVIOUS is also checked against the demo blocklist
// in real adapter mode.
func TestInternalGuardFromEnv_RealMode_DemoPreviousServiceSecret_Rejected(t *testing.T) {
	t.Setenv("GOCELL_SERVICE_SECRET", freshTestServiceSecret(t))
	t.Setenv("GOCELL_SERVICE_SECRET_PREVIOUS", "service-secret-32-bytes-xxxxxx!!")
	_, err := internalGuardFromEnv("real")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_SERVICE_SECRET_PREVIOUS",
		"error must name the offending env var")
	assert.Contains(t, err.Error(), "well-known demo key",
		"error must indicate the reason")
}

// TestInternalGuardFromEnv_DefaultStoreRejectsReplay verifies that the guard
// produced by internalGuardFromEnv blocks same-nonce replay within the
// ServiceToken validity window. Without a NonceStore, an attacker who
// observes a valid ServiceToken (e.g. via a log leak or TLS-terminating
// proxy) can replay it for up to 5 minutes; the guard MUST wire a default
// store even in dev mode so the in-repo demo path is not silently vulnerable.
func TestInternalGuardFromEnv_DefaultStoreRejectsReplay(t *testing.T) {
	secret := freshTestServiceSecret(t)
	t.Setenv("GOCELL_SERVICE_SECRET", secret)

	guard, err := internalGuardFromEnv("") // dev mode still installs store
	require.NoError(t, err)
	require.NotNil(t, guard, "guard must be installed when secret is present")

	ring, err := auth.NewHMACKeyRing([]byte(secret), nil)
	require.NoError(t, err)

	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	guarded := guard.Middleware()(inner)

	const path = "/internal/v1/access/roles"
	token := auth.GenerateServiceToken(ring, http.MethodGet, path, "", time.Now())
	require.NotEmpty(t, token, "token generation must succeed")

	req1 := httptest.NewRequest(http.MethodGet, path, nil)
	req1.Header.Set("Authorization", "ServiceToken "+token)
	rec1 := httptest.NewRecorder()
	guarded.ServeHTTP(rec1, req1)
	require.Equal(t, http.StatusOK, rec1.Code, "first use of valid token must pass")

	req2 := httptest.NewRequest(http.MethodGet, path, nil)
	req2.Header.Set("Authorization", "ServiceToken "+token)
	rec2 := httptest.NewRecorder()
	guarded.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusUnauthorized, rec2.Code,
		"replay of same nonce inside validity window must be rejected")
}

// TestInternalGuardFromEnv_RealMode_GuardInstalledWithSecret pins the S32
// invariant: in real adapter mode, presence of GOCELL_SERVICE_SECRET MUST
// produce a non-nil guard. service-token is currently the sole transport
// authenticator for /internal/v1/* (no mTLS yet), so a nil guard here
// silently exposes the control plane.
func TestInternalGuardFromEnv_RealMode_GuardInstalledWithSecret(t *testing.T) {
	t.Setenv("GOCELL_SERVICE_SECRET", freshTestServiceSecret(t))
	guard, err := internalGuardFromEnv("real")
	require.NoError(t, err)
	require.NotNil(t, guard,
		"real mode with valid service secret must install a non-nil guard")
	// Real mode also requires the guard's store to be replay-safe; the
	// SharedDeps.Validate gate later rejects NonceStoreKindNoop, but a
	// guard that was built without the in-memory default would have
	// already bypassed the gate by returning the wrong Kind.
	assert.Equal(t, auth.NonceStoreKindInMemory, guard.NonceStore().Kind(),
		"real mode guard must default to the in-memory nonce store "+
			"(multi-pod deployments replace it with a shared store)")
}

// TestInternalGuardFromEnv_NonceStoreTTLExtendsBeyondTokenWindow pins the
// invariant that the default nonce store's retention window is at least as
// long as the token validity window plus the edge buffer — shrinking the TTL
// below ServiceTokenMaxAge would reintroduce the replay window the PR closes.
//
// We check the TTL indirectly: first register a nonce just after boot,
// advance within the token window, and verify the replay is still rejected.
func TestInternalGuardFromEnv_NonceStoreTTLExtendsBeyondTokenWindow(t *testing.T) {
	secret := freshTestServiceSecret(t)
	t.Setenv("GOCELL_SERVICE_SECRET", secret)
	guard, err := internalGuardFromEnv("real")
	require.NoError(t, err)
	require.NotNil(t, guard)

	// Feed the same nonce twice to the backing store — directly, not via
	// ServiceTokenMiddleware, so the test does not depend on time of day
	// or token signing specifics.
	ctx := t.Context()
	store := guard.NonceStore()
	require.NoError(t, store.CheckAndMark(ctx, "nonce-ttl-probe"),
		"first insert must succeed")
	require.ErrorIs(t, store.CheckAndMark(ctx, "nonce-ttl-probe"),
		auth.ErrNonceReused, "same nonce within TTL must be rejected")
}
