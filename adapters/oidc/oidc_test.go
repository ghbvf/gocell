package oidc

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/lifecycle"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
)

// Compile-time assertion: Adapter satisfies ManagedResource.
var _ lifecycle.ManagedResource = (*Adapter)(nil)

func TestConfig_Validate(t *testing.T) {
	require.NoError(t, Config{IssuerURL: "https://issuer", ClientID: "id"}.Validate())

	for _, tc := range []struct {
		name   string
		config Config
	}{
		{"missing issuer", Config{ClientID: "id"}},
		{"missing client ID", Config{IssuerURL: "https://issuer"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.config.Validate()
			require.Error(t, err)
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec)
			assert.Equal(t, ErrAdapterOIDCConfig, ec.Code)
		})
	}
}

func TestNew_InvalidConfig(t *testing.T) {
	_, err := New(context.Background(), Config{})
	require.Error(t, err)
}

// TestNew_FailsSyncOnUnreachableIssuer verifies that construction fails
// immediately when the OIDC issuer is unreachable (fail-fast at boot).
func TestNew_FailsSyncOnUnreachableIssuer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
	defer cancel()

	_, err := New(ctx, Config{IssuerURL: "http://127.0.0.1:1", ClientID: "test-client"})
	require.Error(t, err, "New must fail when issuer is unreachable")
}

// TestNew_PopulatesProviderSync verifies that after a successful New, calling
// Provider returns the cached provider immediately without re-discovering.
func TestNew_PopulatesProviderSync(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
	defer cancel()

	adapter, err := New(ctx, Config{IssuerURL: srv.URL, ClientID: "test-client"})
	require.NoError(t, err)

	// Close the server — if discover is called again it would fail.
	srv.Close()

	// Provider must return from cache without network.
	p, err := adapter.Provider(context.Background())
	require.NoError(t, err)
	require.NotNil(t, p)
}

// TestCheckers_OIDCReadyHealthyAfterConstruction verifies that the oidc_ready
// probe returns nil (healthy) after a successful construction.
func TestCheckers_OIDCReadyHealthyAfterConstruction(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
	defer cancel()

	adapter, err := New(ctx, Config{IssuerURL: srv.URL, ClientID: "test-client"})
	require.NoError(t, err)

	checkers := adapter.Checkers()
	require.Contains(t, checkers, "oidc_ready")

	probeCtx, probeCancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
	defer probeCancel()

	err = checkers["oidc_ready"](probeCtx)
	require.NoError(t, err, "oidc_ready probe should pass after successful construction")
}

// TestWorker_ReturnsNil verifies that Worker returns nil (no background
// goroutine; rotation worker is PR-11/A-02).
func TestWorker_ReturnsNil(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
	defer cancel()

	adapter, err := New(ctx, Config{IssuerURL: srv.URL, ClientID: "test-client"})
	require.NoError(t, err)

	assert.Nil(t, adapter.Worker(), "Worker() must return nil until PR-11/A-02")
}

// TestClose_Idempotent verifies that Close is safe to call multiple times.
func TestClose_Idempotent(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
	defer cancel()

	adapter, err := New(ctx, Config{IssuerURL: srv.URL, ClientID: "test-client"})
	require.NoError(t, err)

	require.NoError(t, adapter.Close(context.Background()))
	require.NoError(t, adapter.Close(context.Background()), "second Close must not error")
}

// TestRefresh_StillCallableAfterConstruction verifies that Refresh remains
// callable (PR-11/A-02 JWKS rotation worker will use it internally).
func TestRefresh_StillCallableAfterConstruction(t *testing.T) {
	srv := mockOIDCServer(t)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), testtime.CtxDefault)
	defer cancel()

	adapter, err := New(ctx, Config{IssuerURL: srv.URL, ClientID: "test-client"})
	require.NoError(t, err)

	p, err := adapter.Refresh(context.Background())
	require.NoError(t, err)
	require.NotNil(t, p)
}

// ---------------------------------------------------------------------------
// TLS enforcement tests (F-4: SEC-FAIL-CLOSED on IssuerURL)
// ---------------------------------------------------------------------------

// TestConfigValidate_RejectsNonTLSIssuer verifies that Config.Validate rejects
// a plain-HTTP issuer URL for a remote host (SEC-FAIL-CLOSED).
func TestConfigValidate_RejectsNonTLSIssuer(t *testing.T) {
	t.Parallel()

	err := Config{IssuerURL: "http://idp.example.com", ClientID: "id"}.Validate()
	require.Error(t, err, "Validate must reject non-TLS remote issuer URL")
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec, "error must be an *errcode.Error")
	assert.Equal(t, errcode.ErrAdapterEndpointNotTLS, ec.Code)
}

// TestConfigValidate_AcceptsLoopbackHTTP verifies that loopback IPs are exempt
// from the TLS requirement (dev/CI testcontainer exception).
func TestConfigValidate_AcceptsLoopbackHTTP(t *testing.T) {
	t.Parallel()

	err := Config{IssuerURL: "http://127.0.0.1:8080", ClientID: "id"}.Validate()
	require.NoError(t, err, "Validate must accept http://127.0.0.1:* (loopback exempt)")
}

// TestConfigValidate_AcceptsHTTPS verifies that HTTPS remote issuer URLs are accepted.
func TestConfigValidate_AcceptsHTTPS(t *testing.T) {
	t.Parallel()

	err := Config{IssuerURL: "https://idp.example.com", ClientID: "id"}.Validate()
	require.NoError(t, err, "Validate must accept https:// issuer URL")
}
