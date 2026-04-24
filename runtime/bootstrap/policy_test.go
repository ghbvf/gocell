package bootstrap_test

// policy_test.go — table-driven coverage for bootstrap Policy implementations.
//
// Uses httptest.NewRecorder + ServeHTTP to avoid binding TCP sockets
// (sandbox constraint), so all tests run without network access.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/bootstrap"
	"github.com/go-chi/chi/v5"
	"github.com/stretchr/testify/require"
)

// ---------------------------------------------------------------------------
// Test doubles
// ---------------------------------------------------------------------------

// stubVerifier satisfies auth.IntentTokenVerifier for tests that need a non-nil verifier.
type stubVerifier struct{}

func (stubVerifier) VerifyIntent(_ context.Context, _ string, _ auth.TokenIntent) (auth.Claims, error) {
	return auth.Claims{}, nil
}

// okHandler is a trivial http.Handler that returns 200 OK with body "ok".
var okHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
})

// serveRequest builds a chi.Mux with the policy applied, registers okHandler
// at GET /, then serves a single request and returns the recorded response.
func serveRequest(t *testing.T, p cell.Policy, method, target string, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	mux := chi.NewMux()
	bootstrap.ApplyPolicyForTest(p, mux)
	mux.MethodFunc(method, "/", okHandler.ServeHTTP)

	req := httptest.NewRequest(method, target, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)
	return rr
}

// ---------------------------------------------------------------------------
// PolicyNone
// ---------------------------------------------------------------------------

func TestPolicyNone(t *testing.T) {
	t.Parallel()

	p := bootstrap.PolicyNone()

	t.Run("describe", func(t *testing.T) {
		t.Parallel()
		if got := p.Describe(); got != "none" {
			t.Errorf("Describe() = %q, want %q", got, "none")
		}
	})

	t.Run("passes_all_requests", func(t *testing.T) {
		t.Parallel()
		// serveRequest calls ApplyPolicyForTest → apply (no-op) then registers handler.
		rr := serveRequest(t, p, http.MethodGet, "/", nil)
		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rr.Code)
		}
	})

	t.Run("apply_is_noop", func(t *testing.T) {
		t.Parallel()
		// Explicit apply coverage: build mux, apply, confirm handler reached.
		mux := chi.NewMux()
		bootstrap.ApplyPolicyForTest(p, mux)
		mux.Get("/", okHandler.ServeHTTP)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rr.Code)
		}
	})
}

// ---------------------------------------------------------------------------
// PolicyJWT — constructor fail-fast; middleware wiring tested at auth pkg level
// ---------------------------------------------------------------------------

func TestPolicyJWT(t *testing.T) {
	t.Parallel()

	t.Run("describe", func(t *testing.T) {
		t.Parallel()
		p := bootstrap.PolicyJWT(stubVerifier{})
		if got := p.Describe(); got != "jwt" {
			t.Errorf("Describe() = %q, want %q", got, "jwt")
		}
	})

	t.Run("nil_verifier_panics", func(t *testing.T) {
		t.Parallel()
		defer func() {
			r := recover()
			if r == nil {
				t.Error("expected panic for nil verifier, got none")
			}
		}()
		bootstrap.PolicyJWT(nil)
	})

	t.Run("apply_installs_jwt_middleware_rejects_unauthenticated", func(t *testing.T) {
		t.Parallel()
		// apply() installs AuthMiddleware; requests without a bearer token
		// should receive 401. This covers the apply() code path.
		p := bootstrap.PolicyJWT(stubVerifier{})
		mux := chi.NewMux()
		bootstrap.ApplyPolicyForTest(p, mux)
		mux.Get("/", okHandler.ServeHTTP)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 (no bearer token)", rr.Code)
		}
	})
}

// ---------------------------------------------------------------------------
// PolicyServiceToken — constructor fail-fast
// ---------------------------------------------------------------------------

func TestPolicyServiceToken(t *testing.T) {
	t.Parallel()

	ring, err := auth.NewHMACKeyRing(make([]byte, 32), nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("describe", func(t *testing.T) {
		t.Parallel()
		p := bootstrap.PolicyServiceToken(auth.NewNoopNonceStore(), ring)
		if got := p.Describe(); got != "service-token" {
			t.Errorf("Describe() = %q, want %q", got, "service-token")
		}
	})

	t.Run("nil_store_panics", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for nil store, got none")
			}
		}()
		bootstrap.PolicyServiceToken(nil, ring)
	})

	t.Run("nil_ring_panics", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for nil ring, got none")
			}
		}()
		bootstrap.PolicyServiceToken(auth.NewNoopNonceStore(), nil)
	})

	t.Run("apply_installs_middleware_rejects_unauthenticated", func(t *testing.T) {
		t.Parallel()
		// Apply the policy to a mux and confirm it blocks requests without
		// a service token (401). This covers the apply() code path.
		p := bootstrap.PolicyServiceToken(auth.NewNoopNonceStore(), ring)
		mux := chi.NewMux()
		bootstrap.ApplyPolicyForTest(p, mux)
		mux.Get("/", okHandler.ServeHTTP)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 (no service token)", rr.Code)
		}
	})
}

// ---------------------------------------------------------------------------
// PolicyMTLS — constructor fail-fast + middleware
// ---------------------------------------------------------------------------

func TestPolicyMTLS(t *testing.T) {
	t.Parallel()

	pool := x509.NewCertPool()

	t.Run("describe", func(t *testing.T) {
		t.Parallel()
		p := bootstrap.PolicyMTLS(pool)
		if got := p.Describe(); got != "mtls" {
			t.Errorf("Describe() = %q, want %q", got, "mtls")
		}
	})

	t.Run("nil_pool_panics", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for nil pool, got none")
			}
		}()
		bootstrap.PolicyMTLS(nil)
	})

	t.Run("no_tls_connection_rejected", func(t *testing.T) {
		t.Parallel()
		p := bootstrap.PolicyMTLS(pool)
		// Request with no TLS (r.TLS == nil) — expect 401.
		rr := serveRequest(t, p, http.MethodGet, "/", nil)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 (no mTLS)", rr.Code)
		}
	})

	t.Run("with_mtls_client_auth_option", func(t *testing.T) {
		t.Parallel()
		// Verify WithMTLSClientAuth option does not panic.
		p := bootstrap.PolicyMTLS(pool, bootstrap.WithMTLSClientAuth(tls.RequireAnyClientCert))
		if got := p.Describe(); got != "mtls" {
			t.Errorf("Describe() = %q, want %q", got, "mtls")
		}
		// Also verify middleware still rejects non-TLS requests.
		rr := serveRequest(t, p, http.MethodGet, "/", nil)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rr.Code)
		}
	})
}

// TestPolicyMTLS_HappyPath_ValidCert verifies that a request with a valid peer
// certificate chaining to the CA pool returns 200 OK (TEST-04).
func TestPolicyMTLS_HappyPath_ValidCert(t *testing.T) {
	t.Parallel()

	// Generate self-signed CA + leaf cert entirely in memory.
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "test-ca"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, &caKey.PublicKey, caKey)
	require.NoError(t, err)
	caCert, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: "test-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCert, &leafKey.PublicKey, caKey)
	require.NoError(t, err)
	leafCert, err := x509.ParseCertificate(leafDER)
	require.NoError(t, err)

	// Build CA pool containing only our CA.
	pool := x509.NewCertPool()
	pool.AddCert(caCert)

	p := bootstrap.PolicyMTLS(pool)
	mux := chi.NewMux()
	bootstrap.ApplyPolicyForTest(p, mux)
	mux.MethodFunc(http.MethodGet, "/", okHandler.ServeHTTP)

	// Inject peer certificate into the request's TLS state (bypasses actual TLS).
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{leafCert},
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (valid peer cert chaining to CA pool)", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// PolicyVerboseToken
// ---------------------------------------------------------------------------

func TestPolicyVerboseToken(t *testing.T) {
	t.Parallel()

	const (
		headerName = "X-Readyz-Token"
		token      = "secret-token"
	)

	t.Run("describe", func(t *testing.T) {
		t.Parallel()
		p := bootstrap.PolicyVerboseToken(headerName, token)
		if got := p.Describe(); got != "verbose-token" {
			t.Errorf("Describe() = %q, want %q", got, "verbose-token")
		}
	})

	t.Run("empty_header_panics", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for empty headerName, got none")
			}
		}()
		bootstrap.PolicyVerboseToken("", token)
	})

	t.Run("empty_token_panics", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if r := recover(); r == nil {
				t.Error("expected panic for empty token, got none")
			}
		}()
		bootstrap.PolicyVerboseToken(headerName, "")
	})

	tests := []struct {
		name       string
		url        string
		headers    map[string]string
		wantStatus int
	}{
		{
			name:       "no verbose query — pass through",
			url:        "/",
			wantStatus: http.StatusOK,
		},
		{
			name:       "verbose with matching token — pass through",
			url:        "/?verbose",
			headers:    map[string]string{headerName: token},
			wantStatus: http.StatusOK,
		},
		{
			name:       "verbose with mismatched token — 401",
			url:        "/?verbose",
			headers:    map[string]string{headerName: "wrong"},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "verbose with missing header — 401",
			url:        "/?verbose",
			wantStatus: http.StatusUnauthorized,
		},
	}

	p := bootstrap.PolicyVerboseToken(headerName, token)

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			mux := chi.NewMux()
			bootstrap.ApplyPolicyForTest(p, mux)
			mux.MethodFunc(http.MethodGet, "/", okHandler.ServeHTTP)

			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			for k, v := range tc.headers {
				req.Header.Set(k, v)
			}
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Errorf("status = %d, want %d; body = %s", rr.Code, tc.wantStatus, rr.Body.String())
			}
			if tc.wantStatus == http.StatusUnauthorized {
				// Verify error body structure.
				body, _ := io.ReadAll(rr.Body)
				var errResp map[string]any
				if err := json.Unmarshal(body, &errResp); err != nil {
					t.Errorf("401 body not valid JSON: %v; body = %s", err, body)
				}
				errField, _ := errResp["error"].(map[string]any)
				if errField == nil {
					t.Errorf("401 body missing 'error' field; body = %s", body)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PolicyStack
// ---------------------------------------------------------------------------

func TestPolicyStack(t *testing.T) {
	t.Parallel()

	t.Run("describe", func(t *testing.T) {
		t.Parallel()
		p := bootstrap.PolicyStack(bootstrap.PolicyNone(), bootstrap.PolicyNone())
		if got := p.Describe(); got != "stack[none, none]" {
			t.Errorf("Describe() = %q, want %q", got, "stack[none, none]")
		}
	})

	// Verify PolicyStack with zero elements produces a valid no-op policy:
	// requests pass through without restriction.
	t.Run("empty_stack", func(t *testing.T) {
		t.Parallel()
		p := bootstrap.PolicyStack()
		require.NotNil(t, p, "PolicyStack() must return a non-nil policy")
		require.Equal(t, "stack[]", p.Describe(), "empty stack Describe must be stack[]")

		mux := chi.NewMux()
		bootstrap.ApplyPolicyForTest(p, mux)
		mux.MethodFunc(http.MethodGet, "/", okHandler.ServeHTTP)

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		require.Equal(t, http.StatusOK, rr.Code, "empty stack must pass requests through")
	})

	// Verify PolicyStack(A, B, C) passes requests through all three policies
	// and that the final handler is reached (using PolicyNone as each element).
	t.Run("middleware_order_A_B_C_passes_through", func(t *testing.T) {
		t.Parallel()

		p := bootstrap.PolicyStack(
			bootstrap.PolicyNone(),
			bootstrap.PolicyNone(),
			bootstrap.PolicyNone(),
		)

		mux := chi.NewMux()
		bootstrap.ApplyPolicyForTest(p, mux)
		mux.MethodFunc(http.MethodGet, "/", okHandler.ServeHTTP)

		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200", rr.Code)
		}
	})

	// Verify that a stack with a VerboseToken policy as first element blocks
	// when ?verbose is present and no header is provided.
	t.Run("stack_with_verbose_token_blocks", func(t *testing.T) {
		t.Parallel()

		vp := bootstrap.PolicyVerboseToken("X-Test-Token", "abc")
		p := bootstrap.PolicyStack(vp, bootstrap.PolicyNone())

		mux := chi.NewMux()
		bootstrap.ApplyPolicyForTest(p, mux)
		mux.MethodFunc(http.MethodGet, "/", okHandler.ServeHTTP)

		req := httptest.NewRequest(http.MethodGet, "/?verbose", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)

		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401", rr.Code)
		}
	})
}
