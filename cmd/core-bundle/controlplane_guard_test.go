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
	guarded := guard(inner)

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
