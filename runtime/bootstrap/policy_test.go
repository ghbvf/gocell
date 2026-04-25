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

	t.Run("name", func(t *testing.T) {
		t.Parallel()
		if got := p.Name; got != "none" {
			t.Errorf("Name = %q, want %q", got, "none")
		}
	})

	t.Run("passes_all_requests", func(t *testing.T) {
		t.Parallel()
		// serveRequest calls ApplyPolicyForTest → no-op then registers handler.
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

	t.Run("name", func(t *testing.T) {
		t.Parallel()
		p := bootstrap.PolicyJWT(stubVerifier{})
		if got := p.Name; got != "jwt" {
			t.Errorf("Name = %q, want %q", got, "jwt")
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

	t.Run("middleware_is_nil_extension_carries_verifier", func(t *testing.T) {
		t.Parallel()
		// F3 round-3: PolicyJWT is a marker. Its Middleware is nil because the
		// matcher-aware AuthMiddleware is built by router.WithAuthMiddleware at
		// phase5 — Bootstrap extracts the verifier from Policy.Extension.
		// Standalone application on a mux is a no-op (no auth installed).
		v := stubVerifier{}
		p := bootstrap.PolicyJWT(v)
		if p.Middleware != nil {
			t.Error("PolicyJWT.Middleware must be nil; Bootstrap installs router-aware variant via Extension")
		}
		if p.Extension == nil {
			t.Fatal("PolicyJWT.Extension must carry the verifier for Bootstrap to extract")
		}
		// Standalone apply: no middleware → handler runs unauthenticated, 200.
		mux := chi.NewMux()
		bootstrap.ApplyPolicyForTest(p, mux)
		mux.Get("/", okHandler.ServeHTTP)
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		rr := httptest.NewRecorder()
		mux.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("status = %d, want 200 (PolicyJWT used standalone has no middleware)", rr.Code)
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

	t.Run("name", func(t *testing.T) {
		t.Parallel()
		p := bootstrap.PolicyServiceToken(auth.NewNoopNonceStore(), ring)
		if got := p.Name; got != "service-token" {
			t.Errorf("Name = %q, want %q", got, "service-token")
		}
	})

	t.Run("nil_store_panics", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if recover() == nil {
				t.Error("expected panic for nil store, got none")
			}
		}()
		bootstrap.PolicyServiceToken(nil, ring)
	})

	t.Run("nil_ring_panics", func(t *testing.T) {
		t.Parallel()
		defer func() {
			if recover() == nil {
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
// PolicyMTLS — constructor + peer-cert-presence middleware
// ---------------------------------------------------------------------------
//
// PolicyMTLS post-RES-#11 is a peer-cert-presence guard. Chain verification
// is delegated to the listener's *tls.Config (ClientAuth +
// ClientCAs), enforced by Bootstrap.phase0 via
// validateListenerPolicyMTLSBinding. The policy itself takes no arguments —
// the previously-misleading PolicyMTLS(pool *x509.CertPool, ...) signature
// is gone.

func TestPolicyMTLS(t *testing.T) {
	t.Parallel()

	t.Run("name", func(t *testing.T) {
		t.Parallel()
		p := bootstrap.PolicyMTLS()
		if got := p.Name; got != "mtls" {
			t.Errorf("Name = %q, want %q", got, "mtls")
		}
	})

	t.Run("no_tls_connection_rejected", func(t *testing.T) {
		t.Parallel()
		p := bootstrap.PolicyMTLS()
		rr := serveRequest(t, p, http.MethodGet, "/", nil)
		if rr.Code != http.StatusUnauthorized {
			t.Errorf("status = %d, want 401 (no mTLS)", rr.Code)
		}
	})
}

// TestPolicyMTLS_HappyPath_PeerCertPresent verifies that a request whose
// TLS state already carries a peer certificate (i.e. the handshake-layer
// chain check already passed at this point) reaches the inner handler.
func TestPolicyMTLS_HappyPath_PeerCertPresent(t *testing.T) {
	t.Parallel()

	// Generate a leaf certificate. The handshake-layer chain check is
	// outside this unit test's scope — we simulate "handshake already
	// validated this cert against the CA pool" by populating PeerCertificates
	// directly.
	leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "test-client"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, leafTemplate, &leafKey.PublicKey, leafKey)
	require.NoError(t, err)
	leafCert, err := x509.ParseCertificate(leafDER)
	require.NoError(t, err)

	p := bootstrap.PolicyMTLS()
	mux := chi.NewMux()
	bootstrap.ApplyPolicyForTest(p, mux)
	mux.MethodFunc(http.MethodGet, "/", okHandler.ServeHTTP)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.TLS = &tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{leafCert},
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 (peer cert present; handshake assumed to have validated)", rr.Code)
	}
}

// ---------------------------------------------------------------------------
// PolicyVerboseToken
// ---------------------------------------------------------------------------

const (
	verboseTokenHeader = "X-Readyz-Token"
	verboseTokenSecret = "secret-token"
)

func TestPolicyVerboseToken_Name(t *testing.T) {
	t.Parallel()
	p := bootstrap.PolicyVerboseToken(verboseTokenHeader, verboseTokenSecret)
	if got := p.Name; got != "verbose-token" {
		t.Errorf("Name = %q, want %q", got, "verbose-token")
	}
}

func TestPolicyVerboseToken_EmptyHeaderPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("expected panic for empty headerName, got none")
		}
	}()
	bootstrap.PolicyVerboseToken("", verboseTokenSecret)
}

func TestPolicyVerboseToken_EmptyTokenPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("expected panic for empty token, got none")
		}
	}()
	bootstrap.PolicyVerboseToken(verboseTokenHeader, "")
}

func TestPolicyVerboseToken_RequestStatuses(t *testing.T) {
	t.Parallel()

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
			headers:    map[string]string{verboseTokenHeader: verboseTokenSecret},
			wantStatus: http.StatusOK,
		},
		{
			name:       "verbose with mismatched token — 401",
			url:        "/?verbose",
			headers:    map[string]string{verboseTokenHeader: "wrong"},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "verbose with missing header — 401",
			url:        "/?verbose",
			wantStatus: http.StatusUnauthorized,
		},
	}

	p := bootstrap.PolicyVerboseToken(verboseTokenHeader, verboseTokenSecret)

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			runVerboseTokenCase(t, p, tc.url, tc.headers, tc.wantStatus)
		})
	}
}

// runVerboseTokenCase exercises one PolicyVerboseToken scenario against a
// fresh chi mux. Extracted from TestPolicyVerboseToken_RequestStatuses to keep
// per-case cognitive complexity low.
func runVerboseTokenCase(t *testing.T, p cell.Policy, url string, headers map[string]string, wantStatus int) {
	t.Helper()

	mux := chi.NewMux()
	bootstrap.ApplyPolicyForTest(p, mux)
	mux.MethodFunc(http.MethodGet, "/", okHandler.ServeHTTP)

	req := httptest.NewRequest(http.MethodGet, url, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rr := httptest.NewRecorder()
	mux.ServeHTTP(rr, req)

	if rr.Code != wantStatus {
		t.Errorf("status = %d, want %d; body = %s", rr.Code, wantStatus, rr.Body.String())
	}
	if wantStatus == http.StatusUnauthorized {
		assertVerboseTokenErrorBody(t, rr)
	}
}

// assertVerboseTokenErrorBody validates that a 401 response carries the
// canonical {"error":{"code":"ERR_AUTH_VERBOSE_TOKEN","message":"..."}}
// envelope and the application/json content type. PR-A35 + PR-258 RES-6
// hardening: the response shape is stable wire contract for monitoring /
// SIEM consumers, so we lock down code, message, and content-type.
func assertVerboseTokenErrorBody(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("401 Content-Type = %q, want %q", got, "application/json")
	}
	body, _ := io.ReadAll(rr.Body)
	var errResp map[string]any
	if err := json.Unmarshal(body, &errResp); err != nil {
		t.Errorf("401 body not valid JSON: %v; body = %s", err, body)
		return
	}
	errField, _ := errResp["error"].(map[string]any)
	if errField == nil {
		t.Errorf("401 body missing 'error' field; body = %s", body)
		return
	}
	if got, _ := errField["code"].(string); got != "ERR_AUTH_VERBOSE_TOKEN" {
		t.Errorf("401 error.code = %q, want %q; body = %s",
			got, "ERR_AUTH_VERBOSE_TOKEN", body)
	}
	if got, _ := errField["message"].(string); got == "" {
		t.Errorf("401 error.message must be non-empty; body = %s", body)
	}
}

// ---------------------------------------------------------------------------
// PolicyStack
// ---------------------------------------------------------------------------

func TestPolicyStack(t *testing.T) {
	t.Parallel()

	t.Run("name", func(t *testing.T) {
		t.Parallel()
		p := bootstrap.PolicyStack(bootstrap.PolicyNone(), bootstrap.PolicyNone())
		if got := p.Name; got != "stack[none, none]" {
			t.Errorf("Name = %q, want %q", got, "stack[none, none]")
		}
	})

	// Verify PolicyStack with zero elements produces a valid no-op policy:
	// requests pass through without restriction.
	t.Run("empty_stack", func(t *testing.T) {
		t.Parallel()
		p := bootstrap.PolicyStack()
		require.Equal(t, "stack[]", p.Name, "empty stack Name must be stack[]")

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

	t.Run("jwt_rejected", func(t *testing.T) {
		t.Parallel()

		require.PanicsWithValue(t,
			"bootstrap: PolicyStack does not support PolicyJWT or PolicyJWTFromAssembly; pass JWT directly as the listener default policy",
			func() {
				bootstrap.PolicyStack(bootstrap.PolicyJWT(stubVerifier{}), bootstrap.PolicyNone())
			})
	})
}

// TestPolicyVerboseToken_QueryParamBoundary verifies the boundary values of
// the ?verbose query parameter parsing (policyVerboseActive). The middleware
// must only enforce the token guard when verbose mode is semantically "on";
// negative/falsy values must be treated as "verbose off" and pass through.
// This test covers SEC-06: false positive 401s must be prevented for k8s probes
// that pass ?verbose=false.
func TestPolicyVerboseToken_QueryParamBoundary(t *testing.T) {
	t.Parallel()

	const (
		headerName = "X-Readyz-Token"
		token      = "boundary-test-token"
	)

	tests := []struct {
		name        string
		url         string
		supplyToken bool // whether to include the correct token header
		wantStatus  int
	}{
		// Falsy values — verbose is OFF, middleware is a pass-through regardless of token.
		{name: "verbose=false_no_token_passthrough", url: "/?verbose=false", supplyToken: false, wantStatus: http.StatusOK},
		{name: "verbose=0_no_token_passthrough", url: "/?verbose=0", supplyToken: false, wantStatus: http.StatusOK},
		{name: "verbose=other_no_token_passthrough", url: "/?verbose=nope", supplyToken: false, wantStatus: http.StatusOK},
		// Truthy values — verbose is ON, token required.
		{name: "verbose_bare_no_token_401", url: "/?verbose", supplyToken: false, wantStatus: http.StatusUnauthorized},
		{name: "verbose_equal_no_token_401", url: "/?verbose=", supplyToken: false, wantStatus: http.StatusUnauthorized},
		{name: "verbose=1_no_token_401", url: "/?verbose=1", supplyToken: false, wantStatus: http.StatusUnauthorized},
		{name: "verbose=true_no_token_401", url: "/?verbose=true", supplyToken: false, wantStatus: http.StatusUnauthorized},
		{name: "verbose=TRUE_case_insensitive_no_token_401", url: "/?verbose=TRUE", supplyToken: false, wantStatus: http.StatusUnauthorized},
		// Truthy with correct token — pass.
		{name: "verbose=true_with_token_pass", url: "/?verbose=true", supplyToken: true, wantStatus: http.StatusOK},
		{name: "verbose=1_with_token_pass", url: "/?verbose=1", supplyToken: true, wantStatus: http.StatusOK},
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
			if tc.supplyToken {
				req.Header.Set(headerName, token)
			}
			rr := httptest.NewRecorder()
			mux.ServeHTTP(rr, req)

			if rr.Code != tc.wantStatus {
				t.Errorf("url=%q supplyToken=%v: status = %d, want %d; body = %s",
					tc.url, tc.supplyToken, rr.Code, tc.wantStatus, rr.Body.String())
			}
		})
	}
}
