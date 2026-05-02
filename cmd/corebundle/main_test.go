package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
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
	return "ts-" + fmt.Sprintf("%x", b) // "ts-" prefix makes it recognizably a test secret
}

func setPodReachableHealthAddr(t *testing.T) {
	t.Helper()
	t.Setenv("GOCELL_HTTP_HEALTH_ADDR", ":9091")
}

func TestCorebundleModulesMatchAssemblyMetadataOrder(t *testing.T) {
	root := findRepoRoot(t)
	project, err := metadata.NewParser(root).Parse()
	require.NoError(t, err)
	asm := project.Assemblies["corebundle"]
	require.NotNil(t, asm)

	modules, err := corebundleModules(asm.Cells)
	require.NoError(t, err)
	require.Len(t, modules, len(asm.Cells))

	gotIDs := make([]string, 0, len(modules))
	for _, module := range modules {
		gotIDs = append(gotIDs, module.ID())
	}
	assert.Equal(t, asm.Cells, gotIDs)
	assert.Equal(t, "configcore", gotIDs[0], "configcore must stay first because it owns SharedPGPool creation")
}

func TestCorebundleModulesRejectUnknownCell(t *testing.T) {
	modules, err := corebundleModules([]string{"configcore", "ghostcore"})
	require.Error(t, err)
	assert.Nil(t, modules)
	assert.Contains(t, err.Error(), `unsupported assembly cell "ghostcore"`)
}

// TestLoadKeySet collapses 5 fragmented per-mode tests into a single
// table-driven specification. Real-mode fail-closed coverage (corrupted PEM,
// empty values) added in PR-A64c follow-up R6: prior tests only exercised the
// happy paths and the missing-env case; corrupted-PEM and one-key-only
// rejection paths were not regression-locked.
func TestLoadKeySet(t *testing.T) {
	priv, pub := generateTestPEM(t)
	const corruptedPEM = "-----BEGIN GARBAGE-----\nnot a real key\n-----END GARBAGE-----\n"

	cases := []struct {
		name        string
		mode        string
		envPriv     string
		envPub      string
		envPrevPub  string
		wantErr     bool
		errContains string
	}{
		{
			name: "dev_mode_no_env_generates_ephemeral",
			mode: "",
		},
		{
			name:    "dev_mode_with_env_keys_uses_them",
			mode:    "",
			envPriv: string(priv),
			envPub:  string(pub),
		},
		{
			name: "unknown_mode_falls_back_to_dev",
			// loadKeySet treats any non-"real" mode as dev. Bootstrap normally
			// rejects unknown GOCELL_ADAPTER_MODE before this is reached; this
			// case exists to lock down the fallback in case a new valid mode
			// is added without updating loadKeySet.
			mode: "reall", // deliberate typo
		},
		{
			name:    "real_mode_with_valid_env_succeeds",
			mode:    "real",
			envPriv: string(priv),
			envPub:  string(pub),
		},
		{
			name:        "real_mode_missing_env_fails_closed",
			mode:        "real",
			wantErr:     true,
			errContains: auth.EnvJWTPrivateKey,
		},
		{
			name:        "real_mode_corrupted_priv_pem_fails_closed",
			mode:        "real",
			envPriv:     corruptedPEM,
			envPub:      string(pub),
			wantErr:     true,
			errContains: "real adapter mode requires",
		},
		{
			name:        "real_mode_corrupted_pub_pem_fails_closed",
			mode:        "real",
			envPriv:     string(priv),
			envPub:      corruptedPEM,
			wantErr:     true,
			errContains: "real adapter mode requires",
		},
		{
			name:        "real_mode_priv_only_fails_closed",
			mode:        "real",
			envPriv:     string(priv),
			wantErr:     true,
			errContains: auth.EnvJWTPublicKey,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(auth.EnvJWTPrivateKey, tc.envPriv)
			t.Setenv(auth.EnvJWTPublicKey, tc.envPub)
			t.Setenv(auth.EnvJWTPrevPublicKey, tc.envPrevPub)

			ks, err := loadKeySet(tc.mode, clock.Real())
			if tc.wantErr {
				require.Error(t, err)
				if tc.errContains != "" {
					assert.Contains(t, err.Error(), tc.errContains)
				}
				return
			}
			require.NoError(t, err)
			assert.NotNil(t, ks)
		})
	}
}

func TestRun_DevMode_StartsAndCancels(t *testing.T) {
	// run() with an immediately-canceled context exercises the full assembly
	// path (cells, bootstrap) without needing a real HTTP listener.
	// Default provision mode is "interactive" (no admin at startup). Opt into
	// bootstrap mode to exercise the Lifecycle + credfile wiring that the
	// original test was designed around.
	t.Setenv(AdminProvisionModeEnv, "bootstrap")
	// STATE_DIR is needed in bootstrap mode (default /run/gocell is not
	// writable in CI).
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())
	// GOCELL_JWT_ISSUER and GOCELL_JWT_AUDIENCE are required in all modes (C5).
	t.Setenv("GOCELL_JWT_ISSUER", "gocell-dev-test")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")
	// PR-A35: verbose endpoint is now gated in every mode; this dev-mode
	// smoke test explicitly waives the verbose debug channel.
	t.Setenv("GOCELL_READYZ_VERBOSE_DISABLED", "1")
	// SEC-FAIL-CLOSED: GOCELL_SERVICE_SECRET is now required in all adapter
	// modes (including dev). Provide a fresh test secret to satisfy the guard.
	t.Setenv("GOCELL_SERVICE_SECRET", freshTestServiceSecret(t))

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
	ctx := t.Context()

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

	ctx := t.Context()

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

	ctx := t.Context()

	err := run(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "GOCELL_JWT_AUDIENCE",
		"run() must fail fast when GOCELL_JWT_AUDIENCE is unset")
}

func TestRun_RealMode_MissingAccessCursorKey_FailsFast(t *testing.T) {
	privPEM, pubPEM := generateTestPEM(t)
	t.Setenv("GOCELL_ADAPTER_MODE", "real")
	setPodReachableHealthAddr(t)
	t.Setenv("GOCELL_SINGLE_POD", "1")
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
	t.Setenv("GOCELL_SINGLE_POD", "1") // F1: acknowledge in-memory nonce store in single-pod real mode
	t.Setenv("GOCELL_ACCESSCORE_CURSOR_KEY", "")

	ctx := t.Context()

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
	setPodReachableHealthAddr(t)
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
	t.Setenv("GOCELL_SINGLE_POD", "1") // F1: acknowledge in-memory nonce store in single-pod real mode
	// The trip-wire: verbose token is empty.
	t.Setenv("GOCELL_READYZ_VERBOSE_TOKEN", "")

	ctx := t.Context()

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
	setPodReachableHealthAddr(t)
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
	t.Setenv("GOCELL_SINGLE_POD", "1") // F1: acknowledge in-memory nonce store in single-pod real mode
	// The trip-wire: metrics token is empty.
	t.Setenv("GOCELL_METRICS_TOKEN", "")

	ctx := t.Context()

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
	setPodReachableHealthAddr(t)
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
	t.Setenv("GOCELL_SINGLE_POD", "1")
	// The trip-wire: service secret is empty.
	t.Setenv("GOCELL_SERVICE_SECRET", "")

	ctx := t.Context()

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
	req.Header.Set(metricsAuthHeader, "wrong")
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
	req.Header.Set(metricsAuthHeader, "secret")
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
	// Opt into bootstrap mode to match the original test's startup path.
	t.Setenv(AdminProvisionModeEnv, "bootstrap")
	t.Setenv("GOCELL_STATE_DIR", t.TempDir())
	// GOCELL_JWT_ISSUER and GOCELL_JWT_AUDIENCE required in all modes (C5).
	t.Setenv("GOCELL_JWT_ISSUER", "gocell-dev-test")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")
	// PR-A35: verbose endpoint is now gated in every mode; this smoke test
	// does not exercise /readyz?verbose so it explicitly waives the endpoint.
	t.Setenv("GOCELL_READYZ_VERBOSE_DISABLED", "1")
	// SEC-FAIL-CLOSED: GOCELL_SERVICE_SECRET is now required in all adapter
	// modes (including dev/in-memory). Provide a fresh test secret.
	t.Setenv("GOCELL_SERVICE_SECRET", freshTestServiceSecret(t))

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

// TestBootstrap_UnknownCellAdapterMode_FailsFast verifies that an unrecognized
// GOCELL_CELL_ADAPTER_MODE value causes run() to fail with an informative error
// before attempting any DB connections.
func TestBootstrap_UnknownCellAdapterMode_FailsFast(t *testing.T) {
	t.Setenv("GOCELL_CELL_ADAPTER_MODE", "cassandra")
	// GOCELL_JWT_ISSUER and GOCELL_JWT_AUDIENCE required in all modes (C5);
	// must be set so run() reaches the cell adapter mode validation step.
	t.Setenv("GOCELL_JWT_ISSUER", "gocell-dev-test")
	t.Setenv("GOCELL_JWT_AUDIENCE", "gocell")

	ctx := t.Context()

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
		t.Run(tc.name, func(t *testing.T) {
			privPEM, pubPEM := generateTestPEM(t)
			t.Setenv("GOCELL_ADAPTER_MODE", "real")
			setPodReachableHealthAddr(t)
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
			// F1: single-pod acknowledgement required for in-memory NonceStore in real mode.
			t.Setenv("GOCELL_SINGLE_POD", "1")
			// Trip-wire: replace just one env with a well-known demo value.
			t.Setenv(tc.patch.name, tc.patch.value)

			ctx := t.Context()

			err := run(ctx)
			require.Error(t, err, "real mode must reject env=%s with demo value", tc.patch.name)
			assert.Contains(t, err.Error(), tc.want)
			assert.Contains(t, err.Error(), "well-known demo key")
		})
	}
}

// captureSlogInfoLines installs a JSON slog handler capturing Info-and-above
// records into a buffer for log assertion. The restore must be called via
// t.Cleanup; not concurrency-safe across goroutines.
func captureSlogInfoLines(t *testing.T) (*bytes.Buffer, func()) {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	return &buf, func() { slog.SetDefault(prev) }
}

// guardWithStore builds a test internalGuard backed by the supplied NonceStore,
// keeping the ring/middleware fields populated to mirror production wiring.
func guardWithStore(t *testing.T, store auth.NonceStore) *internalGuard {
	t.Helper()
	ring, err := auth.NewHMACKeyRing([]byte("test-secret-32-bytes-long-padding!"), nil)
	require.NoError(t, err)
	return &internalGuard{
		ring:       ring,
		nonceStore: store,
		mw:         func(h http.Handler) http.Handler { return h },
	}
}

// TestLogSinglePodNonceStoreAcknowledgement_RealSinglePodInMemory_LogsInfo
// verifies that the positive-path Info signal fires when the operator opted
// into single-pod replay protection (GOCELL_SINGLE_POD=1) on real mode with
// the default in-memory nonce store.
func TestLogSinglePodNonceStoreAcknowledgement_RealSinglePodInMemory_LogsInfo(t *testing.T) {
	store, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clock.Real())
	require.NoError(t, err)
	shared := &SharedDeps{
		Topology: bootstrap.Topology{
			AdapterMode:               "real",
			SinglePodReplayProtection: true,
		},
		InternalGuard: guardWithStore(t, store),
	}

	buf, restore := captureSlogInfoLines(t)
	t.Cleanup(restore)

	logSinglePodNonceStoreAcknowledgement(shared)

	out := buf.String()
	require.NotEmpty(t, out, "expected an Info log line; got empty buffer")
	assert.Contains(t, out, "in-memory nonce store acknowledged for single-pod",
		"Info message must announce the acknowledgement so the configuration is auditable")
	assert.Contains(t, out, string(auth.NonceStoreKindInMemory),
		"log must include nonce_store_kind label so dashboards can filter by store kind")
	assert.Contains(t, out, "GOCELL_SINGLE_POD",
		"log note must reference the env switch operators set to enter this branch")
}

// TestLogSinglePodNonceStoreAcknowledgement_NegativePaths_NoInfoLog asserts
// that the Info signal stays silent on every other configuration: dev mode,
// distributed nonce store, multi-pod, or a nil InternalGuard. This protects
// against accidentally turning the acknowledgement into noise on the dev path.
func TestLogSinglePodNonceStoreAcknowledgement_NegativePaths_NoInfoLog(t *testing.T) {
	inMemStore, err := auth.NewInMemoryNonceStore(auth.ServiceTokenNonceTTL, clock.Real())
	require.NoError(t, err)

	cases := []struct {
		name   string
		shared *SharedDeps
	}{
		{
			name: "dev mode (single-pod ack ignored)",
			shared: &SharedDeps{
				Topology: bootstrap.Topology{
					AdapterMode:               "",
					SinglePodReplayProtection: true,
				},
				InternalGuard: guardWithStore(t, inMemStore),
			},
		},
		{
			name: "real mode without GOCELL_SINGLE_POD (multi-pod path; SharedDeps.Validate would reject upstream)",
			shared: &SharedDeps{
				Topology: bootstrap.Topology{
					AdapterMode:               "real",
					SinglePodReplayProtection: false,
				},
				InternalGuard: guardWithStore(t, inMemStore),
			},
		},
		{
			name: "nil internal guard",
			shared: &SharedDeps{
				Topology: bootstrap.Topology{
					AdapterMode:               "real",
					SinglePodReplayProtection: true,
				},
				InternalGuard: nil,
			},
		},
		{
			name:   "nil shared",
			shared: nil,
		},
		{
			// InternalGuard is non-nil but nonceStore is nil (e.g. guard built
			// before a store is wired). Exercises the ns == nil branch in
			// logSinglePodNonceStoreAcknowledgement (main.go) — must stay silent.
			name: "non-nil guard with nil nonce store",
			shared: &SharedDeps{
				Topology: bootstrap.Topology{
					AdapterMode:               "real",
					SinglePodReplayProtection: true,
				},
				InternalGuard: guardWithStore(t, nil),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			buf, restore := captureSlogInfoLines(t)
			t.Cleanup(restore)

			logSinglePodNonceStoreAcknowledgement(tc.shared)

			assert.False(t,
				strings.Contains(buf.String(), "in-memory nonce store acknowledged for single-pod"),
				"acknowledgement must NOT fire for non-matching configurations; got: %s", buf.String())
		})
	}
}
