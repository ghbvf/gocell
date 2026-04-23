package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"syscall"
	"testing"

	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// freshTestServiceSecret returns a cryptographically random 32-byte hex string
// (64 hex chars, all >= MinHMACKeyBytes) for use as GOCELL_SERVICE_SECRET in
// tests. Using a random value prevents accidental inclusion of well-known demo
// keys in test fixtures that could be blacklisted by rejectDemoKey in future.
func freshTestServiceSecret(t *testing.T) string {
	t.Helper()
	b := make([]byte, 32)
	_, err := rand.Read(b)
	require.NoError(t, err, "crypto/rand must not fail in test setup")
	return "ts-" + fmt.Sprintf("%x", b) // "ts-" prefix makes it recognisably a test secret
}

func TestLoadKeySet_DevMode(t *testing.T) {
	t.Setenv(auth.EnvJWTPrivateKey, "")
	t.Setenv(auth.EnvJWTPublicKey, "")
	ks, err := loadKeySet("")
	require.NoError(t, err)
	assert.NotNil(t, ks)
}

func TestLoadKeySet_DevMode_PrefersEnvKeys(t *testing.T) {
	privPEM, pubPEM := generateTestPEM(t)
	t.Setenv(auth.EnvJWTPrivateKey, string(privPEM))
	t.Setenv(auth.EnvJWTPublicKey, string(pubPEM))
	t.Setenv(auth.EnvJWTPrevPublicKey, "")

	ks, err := loadKeySet("") // dev mode, but env keys provided
	require.NoError(t, err)
	assert.NotNil(t, ks)
}

func TestLoadKeySet_RealMode_MissingEnv(t *testing.T) {
	t.Setenv(auth.EnvJWTPrivateKey, "")
	t.Setenv(auth.EnvJWTPublicKey, "")

	_, err := loadKeySet("real")
	require.Error(t, err)
	assert.Contains(t, err.Error(), auth.EnvJWTPrivateKey)
}

func TestLoadKeySet_RealMode_Success(t *testing.T) {
	privPEM, pubPEM := generateTestPEM(t)
	t.Setenv(auth.EnvJWTPrivateKey, string(privPEM))
	t.Setenv(auth.EnvJWTPublicKey, string(pubPEM))
	t.Setenv(auth.EnvJWTPrevPublicKey, "") // no previous key

	ks, err := loadKeySet("real")
	require.NoError(t, err)
	assert.NotNil(t, ks)
}

func TestLoadKeySet_UnknownMode_StillGeneratesEphemeral(t *testing.T) {
	// loadKeySet treats any non-"real" mode as dev (ephemeral key pair).
	// In practice, bootstrap.TopologyFromEnv rejects unknown GOCELL_ADAPTER_MODE
	// values before loadKeySet is called, so this path is only reachable if a
	// new valid mode is added without updating loadKeySet.
	ks, err := loadKeySet("reall") // deliberate typo
	require.NoError(t, err)
	assert.NotNil(t, ks)
}

func TestLoadSecret_WithEnv(t *testing.T) {
	t.Setenv("TEST_KEY_FOR_ENVDEFAULT", "actual-value")
	got, err := loadSecret("TEST_KEY_FOR_ENVDEFAULT", "fallback", "")
	require.NoError(t, err)
	assert.Equal(t, []byte("actual-value"), got)
}

func TestLoadSecret_DevMode_Fallback(t *testing.T) {
	t.Setenv("TEST_KEY_FOR_ENVDEFAULT_MISS", "")
	got, err := loadSecret("TEST_KEY_FOR_ENVDEFAULT_MISS", "fallback", "")
	require.NoError(t, err)
	assert.Equal(t, []byte("fallback"), got)
}

func TestLoadSecret_RealMode_MissingEnv(t *testing.T) {
	t.Setenv("TEST_KEY_REAL_MISS", "")
	_, err := loadSecret("TEST_KEY_REAL_MISS", "fallback", "real")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "TEST_KEY_REAL_MISS")
	assert.Contains(t, err.Error(), "real")
}

func TestLoadSecret_RealMode_WithEnv(t *testing.T) {
	t.Setenv("TEST_KEY_REAL_OK", "prod-secret")
	got, err := loadSecret("TEST_KEY_REAL_OK", "fallback", "real")
	require.NoError(t, err)
	assert.Equal(t, []byte("prod-secret"), got)
}

func TestRun_DevMode_StartsAndCancels(t *testing.T) {
	// run() with an immediately-cancelled context exercises the full assembly
	// path (cells, bootstrap) without needing a real HTTP listener.
	// Set GOCELL_STATE_DIR to a writable temp dir so WithInitialAdminBootstrap
	// can write the credential file (default /run/gocell is not writable in CI).
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())
	// GOCELL_JWT_ISSUER and GOCELL_JWT_AUDIENCE are required in all modes (C5).
	t.Setenv("GOCELL_JWT_ISSUER", "gocell-dev-test")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately — run() should exit cleanly

	err := run(ctx)
	// Only context.Canceled and listen/sandbox errors are acceptable.
	// Any other error signals a real startup regression.
	if err != nil {
		acceptable := errors.Is(err, context.Canceled) ||
			errors.Is(err, syscall.EPERM) ||
			isBindError(err)
		if !acceptable {
			t.Fatalf("unexpected startup error (not context-canceled or sandbox): %v", err)
		}
	}
}

// isBindError reports whether err wraps a net.OpError with Op "listen".
// This covers "bind: address already in use" and similar listen failures.
func isBindError(err error) bool {
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return opErr.Op == "listen"
	}
	return false
}

func TestRun_InvalidAdapterMode_ReturnsError(t *testing.T) {
	t.Setenv("GOCELL_ADAPTER_MODE", "production")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "adapter mode")
}

// TestRun_MissingJWTIssuer_FailsFast verifies that run() fails fast when
// GOCELL_JWT_ISSUER is unset. The env var is required in all adapter modes (C5).
func TestRun_MissingJWTIssuer_FailsFast(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")
	t.Setenv("GOCELL_AUDITCORE_HMAC_KEY", "dev-hmac-key-replace-in-prod!!!!")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_JWT_ISSUER",
		"run() must fail fast when GOCELL_JWT_ISSUER is unset")
}

// TestRun_MissingJWTAudience_FailsFast verifies that run() fails fast when
// GOCELL_JWT_AUDIENCE is unset. The env var is required in all adapter modes (C5).
func TestRun_MissingJWTAudience_FailsFast(t *testing.T) {
	t.Setenv("GOCELL_JWT_ISSUER", "gocell-dev-test")
	t.Setenv("GOCELL_JWT_AUDIENCE", "")
	t.Setenv("GOCELL_AUDITCORE_HMAC_KEY", "dev-hmac-key-replace-in-prod!!!!")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_JWT_AUDIENCE",
		"run() must fail fast when GOCELL_JWT_AUDIENCE is unset")
}

func TestRun_RealMode_MissingAccessCursorKey_FailsFast(t *testing.T) {
	privPEM, pubPEM := generateTestPEM(t)
	t.Setenv("GOCELL_ADAPTER_MODE", "real")
	t.Setenv(auth.EnvJWTPrivateKey, string(privPEM))
	t.Setenv(auth.EnvJWTPublicKey, string(pubPEM))
	t.Setenv(auth.EnvJWTPrevPublicKey, "")
	t.Setenv("GOCELL_AUDITCORE_HMAC_KEY", "prod-hmac-key-replace-32bytes!!!")
	t.Setenv("GOCELL_JWT_ISSUER", "gocell-real-test")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")
	t.Setenv("GOCELL_AUDITCORE_CURSOR_KEY", "audit-cursor-key-32-bytes-padded!")
	t.Setenv("GOCELL_CONFIGCORE_CURSOR_KEY", "config-cursor-key-32b-padded-xx!")
	t.Setenv("GOCELL_SERVICE_SECRET", freshTestServiceSecret(t))
	t.Setenv("GOCELL_READYZ_VERBOSE_TOKEN", "readyz-token-present")
	t.Setenv("GOCELL_METRICS_TOKEN", "metrics-token-present")
	t.Setenv("GOCELL_ACCESSCORE_CURSOR_KEY", "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_ACCESSCORE_CURSOR_KEY")
}

// TestRun_RealMode_MissingVerboseToken_FailsFast ensures the H1-6
// READYZ-VERBOSE-TOKEN fail-fast integration point — empty
// GOCELL_READYZ_VERBOSE_TOKEN in real mode must error out before the
// HTTP server starts. Guards against reordering inside run() that could
// bypass the check.
func TestRun_RealMode_MissingVerboseToken_FailsFast(t *testing.T) {
	privPEM, pubPEM := generateTestPEM(t)
	t.Setenv("GOCELL_ADAPTER_MODE", "real")
	t.Setenv(auth.EnvJWTPrivateKey, string(privPEM))
	t.Setenv(auth.EnvJWTPublicKey, string(pubPEM))
	t.Setenv(auth.EnvJWTPrevPublicKey, "")
	// Secrets required in real mode (would otherwise fail earlier than
	// the verbose-token check; we want verbose-token to be the trip-wire).
	t.Setenv("GOCELL_AUDITCORE_HMAC_KEY", "prod-hmac-key-replace-32bytes!!!")
	// GOCELL_JWT_ISSUER and GOCELL_JWT_AUDIENCE required in all modes (C5).
	t.Setenv("GOCELL_JWT_ISSUER", "gocell-real-test")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")
	t.Setenv("GOCELL_AUDITCORE_CURSOR_KEY", "audit-cursor-key-32-bytes-padded!")
	t.Setenv("GOCELL_CONFIGCORE_CURSOR_KEY", "config-cursor-key-32b-padded-xx!")
	t.Setenv("GOCELL_ACCESSCORE_CURSOR_KEY", "access-cursor-key-32b-padded-x!!")
	t.Setenv("GOCELL_SERVICE_SECRET", freshTestServiceSecret(t))
	// The trip-wire: verbose token is empty.
	t.Setenv("GOCELL_READYZ_VERBOSE_TOKEN", "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_READYZ_VERBOSE_TOKEN",
		"real mode must fail fast when verbose token is unset")
}

// TestRun_RealMode_MissingMetricsToken_FailsFast mirrors the
// VERBOSE_TOKEN fail-fast pattern: in real mode, unrestricted /metrics
// would expose cell lifecycle signals anonymously, so GOCELL_METRICS_TOKEN
// is required before the HTTP server starts.
func TestRun_RealMode_MissingMetricsToken_FailsFast(t *testing.T) {
	privPEM, pubPEM := generateTestPEM(t)
	t.Setenv("GOCELL_ADAPTER_MODE", "real")
	t.Setenv(auth.EnvJWTPrivateKey, string(privPEM))
	t.Setenv(auth.EnvJWTPublicKey, string(pubPEM))
	t.Setenv(auth.EnvJWTPrevPublicKey, "")
	t.Setenv("GOCELL_AUDITCORE_HMAC_KEY", "prod-hmac-key-replace-32bytes!!!")
	// GOCELL_JWT_ISSUER and GOCELL_JWT_AUDIENCE required in all modes (C5).
	t.Setenv("GOCELL_JWT_ISSUER", "gocell-real-test")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")
	t.Setenv("GOCELL_AUDITCORE_CURSOR_KEY", "audit-cursor-key-32-bytes-padded!")
	t.Setenv("GOCELL_CONFIGCORE_CURSOR_KEY", "config-cursor-key-32b-padded-xx!")
	t.Setenv("GOCELL_ACCESSCORE_CURSOR_KEY", "access-cursor-key-32b-padded-x!!")
	t.Setenv("GOCELL_SERVICE_SECRET", freshTestServiceSecret(t))
	t.Setenv("GOCELL_READYZ_VERBOSE_TOKEN", "readyz-token-present")
	// The trip-wire: metrics token is empty.
	t.Setenv("GOCELL_METRICS_TOKEN", "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_METRICS_TOKEN",
		"real mode must fail fast when metrics token is unset")
}

// TestRun_RealMode_MissingServiceSecret_FailsFast verifies that in real mode
// run() fails fast when GOCELL_SERVICE_SECRET is unset. The secret is required
// in real mode to protect /internal/v1/* paths.
func TestRun_RealMode_MissingServiceSecret_FailsFast(t *testing.T) {
	privPEM, pubPEM := generateTestPEM(t)
	t.Setenv("GOCELL_ADAPTER_MODE", "real")
	t.Setenv(auth.EnvJWTPrivateKey, string(privPEM))
	t.Setenv(auth.EnvJWTPublicKey, string(pubPEM))
	t.Setenv(auth.EnvJWTPrevPublicKey, "")
	t.Setenv("GOCELL_AUDITCORE_HMAC_KEY", "prod-hmac-key-replace-32bytes!!!")
	// GOCELL_JWT_ISSUER and GOCELL_JWT_AUDIENCE required in all modes (C5).
	t.Setenv("GOCELL_JWT_ISSUER", "gocell-real-test")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")
	t.Setenv("GOCELL_AUDITCORE_CURSOR_KEY", "audit-cursor-key-32-bytes-padded!")
	t.Setenv("GOCELL_CONFIGCORE_CURSOR_KEY", "config-cursor-key-32b-padded-xx!")
	t.Setenv("GOCELL_ACCESSCORE_CURSOR_KEY", "access-cursor-key-32b-padded-x!!")
	t.Setenv("GOCELL_READYZ_VERBOSE_TOKEN", "readyz-token-present")
	t.Setenv("GOCELL_METRICS_TOKEN", "metrics-token-present")
	// The trip-wire: service secret is empty.
	t.Setenv("GOCELL_SERVICE_SECRET", "")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_SERVICE_SECRET",
		"real mode must fail fast when service secret is unset")
}

func TestMetricsTokenGuard_RejectsMissingToken(t *testing.T) {
	sentinel := "inner-handler-ran"
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sentinel))
	})
	guarded := withMetricsTokenGuard("secret", inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	guarded.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.NotContains(t, rec.Body.String(), sentinel, "inner handler must not run without token")
}

func TestMetricsTokenGuard_RejectsWrongToken(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	guarded := withMetricsTokenGuard("secret", inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set(metricsTokenHeader, "wrong")
	guarded.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestMetricsTokenGuard_AcceptsCorrectToken(t *testing.T) {
	sentinel := "inner-handler-ran"
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(sentinel))
	})
	guarded := withMetricsTokenGuard("secret", inner)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.Header.Set(metricsTokenHeader, "secret")
	guarded.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), sentinel)
}

// generateTestPEM creates a fresh 2048-bit RSA key pair as PEM bytes.
func generateTestPEM(t *testing.T) (privPEM, pubPEM []byte) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	privPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	})
	pubBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	require.NoError(t, err)
	pubPEM = pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: pubBytes,
	})
	return privPEM, pubPEM
}

// TestBootstrap_DemoModeUsesInMemory verifies that when GOCELL_CELL_ADAPTER_MODE
// is unset (or empty), run() selects the in-memory storage path for configcore
// and does not attempt to connect to PostgreSQL (no GOCELL_CONFIGCORE_DATABASE_URL required).
// Guards against regression where the default could be accidentally flipped to
// "postgres" and break dev/test setups.
func TestBootstrap_DemoModeUsesInMemory(t *testing.T) {
	// Ensure GOCELL_CELL_ADAPTER_MODE is unset (selects in-memory path).
	// GOCELL_CONFIGCORE_DATABASE_URL is not read in memory mode — no DSN required.
	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "")
	// Set GOCELL_STATE_DIR to a writable temp dir so WithInitialAdminBootstrap
	// can write the credential file (default /run/gocell is not writable in CI).
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())
	// GOCELL_JWT_ISSUER and GOCELL_JWT_AUDIENCE required in all modes (C5).
	t.Setenv("GOCELL_JWT_ISSUER", "gocell-dev-test")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately — we only need Init(), not server start

	err := run(ctx)
	// Only context.Canceled and listen/sandbox errors are acceptable.
	// A postgres connection failure would be: "configcore PG pool: ..."
	if err != nil {
		acceptable := errors.Is(err, context.Canceled) ||
			errors.Is(err, syscall.EPERM) ||
			isBindError(err)
		if !acceptable {
			t.Fatalf("unexpected error when GOCELL_CELL_ADAPTER_MODE is empty (should use in-memory): %v", err)
		}
	}
}

// TestBootstrap_UnknownCellAdapterMode_FailsFast verifies that an unrecognised
// GOCELL_CELL_ADAPTER_MODE value causes run() to fail with an informative error
// before attempting any DB connections.
func TestBootstrap_UnknownCellAdapterMode_FailsFast(t *testing.T) {
	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "cassandra")
	// GOCELL_JWT_ISSUER and GOCELL_JWT_AUDIENCE required in all modes (C5);
	// must be set so run() reaches the cell adapter mode validation step.
	t.Setenv("GOCELL_JWT_ISSUER", "gocell-dev-test")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	err := run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "cassandra",
		"error must mention the unknown value")
}

// TestRun_RealMode_DemoKey_FailsFast locks the rejectDemoKey wiring: for
// each env channel (HMAC key + three cursor keys), injecting a well-known
// demo value must abort run() before the HTTP server starts. Guards
// against reordering that would let demo secrets leak into real mode.
// ref: K8s kube-apiserver — refuses to start with insecure signing material.
func TestRun_RealMode_DemoKey_FailsFast(t *testing.T) {
	freshHMAC := "prod-hmac-key-replace-32bytes!!!"
	freshAudit := "audit-cursor-key-32-bytes-padded!"
	freshConfig := "config-cursor-key-32b-padded-xx!"
	freshAccess := "access-cursor-key-32b-padded-x!!"

	type envPatch struct {
		name, value string
	}
	tests := []struct {
		name  string
		patch envPatch
		want  string
	}{
		{
			name:  "HMAC demo literal rejected",
			patch: envPatch{"GOCELL_AUDITCORE_HMAC_KEY", "dev-hmac-key-replace-in-prod!!!!"},
			want:  "GOCELL_AUDITCORE_HMAC_KEY",
		},
		{
			name:  "audit cursor demo literal rejected",
			patch: envPatch{"GOCELL_AUDITCORE_CURSOR_KEY", "corebundle-audit-cursor-key-32b!"},
			want:  "GOCELL_AUDITCORE_CURSOR_KEY",
		},
		{
			name:  "config cursor demo literal rejected",
			patch: envPatch{"GOCELL_CONFIGCORE_CURSOR_KEY", "corebundle-cfg-cursor-key--32bb!"},
			want:  "GOCELL_CONFIGCORE_CURSOR_KEY",
		},
		{
			name:  "access cursor demo literal rejected",
			patch: envPatch{"GOCELL_ACCESSCORE_CURSOR_KEY", "corebundle-access-cursor-key32!!"},
			want:  "GOCELL_ACCESSCORE_CURSOR_KEY",
		},
		{
			name:  "access cursor cell demo literal rejected",
			patch: envPatch{"GOCELL_ACCESSCORE_CURSOR_KEY", "gocell-demo-ACCESS-CORE-key-32!!"},
			want:  "GOCELL_ACCESSCORE_CURSOR_KEY",
		},
		{
			name:  "service secret demo literal rejected",
			patch: envPatch{"GOCELL_SERVICE_SECRET", "service-secret-32-bytes-xxxxxx!!"},
			want:  "GOCELL_SERVICE_SECRET",
		},
		{
			name:  "service secret previous demo literal rejected",
			patch: envPatch{"GOCELL_SERVICE_SECRET_PREVIOUS", "service-secret-32-bytes-xxxxxx!!"},
			want:  "GOCELL_SERVICE_SECRET_PREVIOUS",
		},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			privPEM, pubPEM := generateTestPEM(t)
			t.Setenv("GOCELL_ADAPTER_MODE", "real")
			t.Setenv(auth.EnvJWTPrivateKey, string(privPEM))
			t.Setenv(auth.EnvJWTPublicKey, string(pubPEM))
			t.Setenv(auth.EnvJWTPrevPublicKey, "")
			t.Setenv("GOCELL_AUDITCORE_HMAC_KEY", freshHMAC)
			// GOCELL_JWT_ISSUER and GOCELL_JWT_AUDIENCE required in all modes (C5).
			t.Setenv("GOCELL_JWT_ISSUER", "gocell-real-test")
			t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")
			t.Setenv("GOCELL_AUDITCORE_CURSOR_KEY", freshAudit)
			t.Setenv("GOCELL_CONFIGCORE_CURSOR_KEY", freshConfig)
			t.Setenv("GOCELL_ACCESSCORE_CURSOR_KEY", freshAccess)
			t.Setenv("GOCELL_SERVICE_SECRET", freshTestServiceSecret(t))
			t.Setenv("GOCELL_READYZ_VERBOSE_TOKEN", "readyz-token-present")
			t.Setenv("GOCELL_METRICS_TOKEN", "metrics-token-present")
			// Trip-wire: replace just one env with a well-known demo value.
			t.Setenv(tc.patch.name, tc.patch.value)

			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			err := run(ctx)
			require.Error(t, err, "real mode must reject env=%s with demo value", tc.patch.name)
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), "well-known demo key")
		})
	}
}
