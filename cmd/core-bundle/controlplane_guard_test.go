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
	t.Setenv("GOCELL_SERVICE_SECRET", "service-secret-32-bytes-xxxxxx!!")
	guard, err := internalGuardFromEnv("")
	require.NoError(t, err)
	assert.NotNil(t, guard, "non-empty secret must produce a non-nil guard")
}

func TestInternalGuardFromEnv_WithSecret_GuardRejects401WhenNoHeader(t *testing.T) {
	t.Setenv("GOCELL_SERVICE_SECRET", "service-secret-32-bytes-xxxxxx!!")
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
