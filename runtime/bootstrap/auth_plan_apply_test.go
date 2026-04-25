package bootstrap

// auth_plan_apply_test.go — white-box table-driven tests for applyListenerAuthChain,
// applyGroupAuth, mtlsMiddleware, verboseTokenMiddleware, and
// runAuthPlanValidateHooks (phase4 discovery). Uses package bootstrap (not
// bootstrap_test) for white-box access to unexported helpers.

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
	routerpkg "github.com/ghbvf/gocell/runtime/http/router"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Stubs ────────────────────────────────────────────────────────────────────

// applyStubVerifier satisfies cell.IntentTokenVerifier / auth.IntentTokenVerifier.
type applyStubVerifier struct{}

func (v *applyStubVerifier) VerifyIntent(_ context.Context, _ string, _ cell.TokenIntent) (cell.Claims, error) {
	return cell.Claims{}, nil
}

// applyStubNonceStore satisfies cell.NonceStore.
type applyStubNonceStore struct{}

func (s *applyStubNonceStore) CheckAndMark(_ context.Context, _ string) error { return nil }
func (s *applyStubNonceStore) Kind() cell.NonceStoreKind                      { return cell.NonceStoreKindInMemory }

// applyStubHMACKeyring satisfies cell.HMACKeyring.
type applyStubHMACKeyring struct{}

func (k *applyStubHMACKeyring) Current() []byte   { return []byte("secret-32-bytes-padding-here----") }
func (k *applyStubHMACKeyring) Secrets() [][]byte { return [][]byte{k.Current()} }

// applyStubAssemblyRef satisfies cell.AssemblyRef.
type applyStubAssemblyRef struct {
	id      string
	cellIDs []string
}

func (a *applyStubAssemblyRef) ID() string        { return a.id }
func (a *applyStubAssemblyRef) CellIDs() []string { return a.cellIDs }

// ─── Helpers ──────────────────────────────────────────────────────────────────

// newMinimalBootstrap creates a Bootstrap with no assembly for use in apply tests.
func newMinimalBootstrap() *Bootstrap {
	return &Bootstrap{
		listenerConfigs: make(map[cell.ListenerRef]listenerConfig),
	}
}

// hasAuthMiddlewareOpt returns true if opts contains ≥1 router option.
// applyListenerAuthChain only populates routerOpts for JWT plans (AuthJWT/AuthJWTFromAssembly);
// non-JWT plans add to mws instead. A non-empty routerOpts slice signals a JWT plan.
func hasAuthMiddlewareOpt(opts []routerpkg.Option) bool {
	return len(opts) > 0
}

// ─── TestApplyListenerAuthChain_EachKind ──────────────────────────────────────

func TestApplyListenerAuthChain_EachKind(t *testing.T) {
	t.Parallel()

	verifier := &applyStubVerifier{}
	store := &applyStubNonceStore{}
	ring := &applyStubHMACKeyring{}
	asm := &applyStubAssemblyRef{id: "test-asm"}

	resolvedPlan := cell.NewAuthJWTFromAssembly(asm)
	resolvedPlan.SetResolved(verifier)

	ref := cell.PrimaryListener

	tests := []struct {
		name          string
		chain         []cell.ListenerAuth
		wantMWCount   int
		wantRouterOpt bool // whether a WithAuthMiddleware router option is included
		wantDescribe  string
		wantErr       bool
	}{
		{
			name:          "AuthNone",
			chain:         []cell.ListenerAuth{cell.AuthNone{}},
			wantMWCount:   0,
			wantRouterOpt: false,
			wantDescribe:  "none",
		},
		{
			name:          "AuthJWT",
			chain:         []cell.ListenerAuth{cell.NewAuthJWT(verifier)},
			wantMWCount:   0,
			wantRouterOpt: true,
			wantDescribe:  "jwt",
		},
		{
			name:          "AuthJWTFromAssembly_resolved",
			chain:         []cell.ListenerAuth{resolvedPlan},
			wantMWCount:   0,
			wantRouterOpt: true,
			wantDescribe:  "jwt",
		},
		{
			name: "AuthJWTFromAssembly_unresolved",
			chain: []cell.ListenerAuth{
				cell.NewAuthJWTFromAssembly(asm), // not SetResolved
			},
			wantErr: true,
		},
		{
			name:          "AuthMTLS",
			chain:         []cell.ListenerAuth{cell.AuthMTLS{}},
			wantMWCount:   1,
			wantRouterOpt: false,
			wantDescribe:  "mtls",
		},
		{
			name:          "AuthServiceToken",
			chain:         []cell.ListenerAuth{cell.NewAuthServiceToken(store, ring)},
			wantMWCount:   1,
			wantRouterOpt: false,
			wantDescribe:  "service-token",
		},
		{
			name: "MultiPlan_MTLSAndServiceToken",
			chain: []cell.ListenerAuth{
				cell.AuthMTLS{},
				cell.NewAuthServiceToken(store, ring),
			},
			wantMWCount:   2,
			wantRouterOpt: false,
			wantDescribe:  "mtls+service-token",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := newMinimalBootstrap()
			mws, routerOpts, describe, err := b.applyListenerAuthChain(ref, tc.chain)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Len(t, mws, tc.wantMWCount, "middleware count")
			assert.Equal(t, tc.wantDescribe, describe, "describe")
			if tc.wantRouterOpt {
				assert.True(t, hasAuthMiddlewareOpt(routerOpts),
					"expected WithAuthMiddleware router option but none found")
			} else {
				assert.False(t, hasAuthMiddlewareOpt(routerOpts),
					"expected no WithAuthMiddleware router option but found one")
			}
		})
	}
}

// ─── TestApplyGroupAuth_EachKind ──────────────────────────────────────────────

func TestApplyGroupAuth_EachKind(t *testing.T) {
	t.Parallel()

	store := &applyStubNonceStore{}
	ring := &applyStubHMACKeyring{}

	tests := []struct {
		name      string
		plan      cell.GroupAuth
		wantNilMW bool
		wantErr   bool
	}{
		{
			name:      "nil_plan",
			plan:      nil,
			wantNilMW: true,
		},
		{
			name:      "AuthNone",
			plan:      cell.AuthNone{},
			wantNilMW: true,
		},
		{
			name:      "AuthMTLS",
			plan:      cell.AuthMTLS{},
			wantNilMW: false,
		},
		{
			name:      "AuthServiceToken",
			plan:      cell.NewAuthServiceToken(store, ring),
			wantNilMW: false,
		},
		{
			name:      "AuthVerboseToken",
			plan:      cell.NewAuthVerboseToken("X-Token", "secret"),
			wantNilMW: false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mw, err := applyGroupAuth(tc.plan)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			if tc.wantNilMW {
				assert.Nil(t, mw, "expected nil middleware")
			} else {
				assert.NotNil(t, mw, "expected non-nil middleware")
			}
		})
	}
}

// ─── TestMtlsMiddleware_PeerCertPresence ──────────────────────────────────────

func TestMtlsMiddleware_PeerCertPresence(t *testing.T) {
	t.Parallel()

	mw := mtlsMiddleware()
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	t.Run("no_TLS_state_returns_401", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		// req.TLS is nil by default
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("TLS_with_no_peer_certs_returns_401", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.TLS = &tls.ConnectionState{} // empty, no peer certs
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		assert.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("TLS_with_peer_cert_returns_200", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.TLS = &tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{{}}, // presence is enough
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		assert.Equal(t, http.StatusOK, w.Code)
	})
}

// ─── TestVerboseTokenMiddleware_QueryParamBoundary ────────────────────────────

func TestVerboseTokenMiddleware_QueryParamBoundary(t *testing.T) {
	t.Parallel()

	const headerName = "X-Readyz-Token"
	const token = "correct-secret"
	hashedToken := sha256.Sum256([]byte(token))

	mw := verboseTokenMiddleware(headerName, hashedToken)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name       string
		url        string
		headerVal  string
		wantStatus int
	}{
		{
			name:       "no_verbose_param_passes_through",
			url:        "/readyz",
			wantStatus: http.StatusOK,
		},
		{
			name:       "verbose_false_passes_through",
			url:        "/readyz?verbose=false",
			wantStatus: http.StatusOK,
		},
		{
			name:       "verbose_true_without_token_401",
			url:        "/readyz?verbose=true",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "verbose_true_wrong_token_401",
			url:        "/readyz?verbose=true",
			headerVal:  "wrong-token",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "verbose_true_correct_token_200",
			url:        "/readyz?verbose=true",
			headerVal:  token,
			wantStatus: http.StatusOK,
		},
		{
			name:       "verbose_1_without_token_401",
			url:        "/readyz?verbose=1",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "verbose_1_correct_token_200",
			url:        "/readyz?verbose=1",
			headerVal:  token,
			wantStatus: http.StatusOK,
		},
		{
			// ?verbose= (empty value) is truthy per probequery.Verbose; without
			// a token the middleware returns 401.
			name:       "verbose_empty_value_requires_token",
			url:        "/readyz?verbose=",
			wantStatus: http.StatusUnauthorized,
		},
		{
			name:       "verbose_0_passes_through",
			url:        "/readyz?verbose=0",
			wantStatus: http.StatusOK,
		},
		{
			name:       "empty_token_header_with_verbose_401",
			url:        "/readyz?verbose=true",
			headerVal:  "",
			wantStatus: http.StatusUnauthorized,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodGet, tc.url, nil)
			if tc.headerVal != "" {
				req.Header.Set(headerName, tc.headerVal)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			assert.Equal(t, tc.wantStatus, w.Code)
		})
	}
}

// ─── TestVerboseTokenMiddleware_TokenComparison ───────────────────────────────

func TestVerboseTokenMiddleware_TokenComparison(t *testing.T) {
	t.Parallel()

	const headerName = "X-Readyz-Token"
	const token = "my-secret-token"
	hashedToken := sha256.Sum256([]byte(token))

	mw := verboseTokenMiddleware(headerName, hashedToken)
	handler := mw(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	// The middleware must use SHA-256 comparison: verify that providing the raw hash
	// as the header value does NOT authenticate (SHA-256(hash) != hash).
	rawHash := make([]byte, 32)
	copy(rawHash, hashedToken[:])

	req := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)
	req.Header.Set(headerName, string(rawHash))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code,
		"providing the raw SHA-256 hash as the token value must not authenticate")

	// Correct token must authenticate.
	req2 := httptest.NewRequest(http.MethodGet, "/readyz?verbose=true", nil)
	req2.Header.Set(headerName, token)
	w2 := httptest.NewRecorder()
	handler.ServeHTTP(w2, req2)
	assert.Equal(t, http.StatusOK, w2.Code, "correct token must authenticate")
}

// ─── TestRunAuthPlanValidateHooks_DiscoverScenarios ───────────────────────────

// fakeAuthProviderCell implements cell.Cell (via embedded BaseCell) and
// cell.AuthProvider so it can be used as a fake auth-provider cell in
// runAuthPlanValidateHooks tests.
type fakeAuthProviderCell struct {
	*cell.BaseCell
	verifier auth.IntentTokenVerifier
}

func newFakeAuthCell(id string, v auth.IntentTokenVerifier) *fakeAuthProviderCell {
	base := cell.NewBaseCell(cell.CellMetadata{
		ID:               id,
		Type:             cell.CellTypeCore,
		ConsistencyLevel: cell.L1,
	})
	return &fakeAuthProviderCell{BaseCell: base, verifier: v}
}

func (c *fakeAuthProviderCell) TokenVerifier() cell.IntentTokenVerifier { return c.verifier }

// Ensure fakeAuthProviderCell satisfies cell.AuthProvider at compile time.
var _ cell.AuthProvider = (*fakeAuthProviderCell)(nil)

// fakeAssemblyWithCells satisfies both cell.AssemblyRef and assemblyWithCell.
type fakeAssemblyWithCells struct {
	id    string
	cells map[string]cell.Cell
}

func (a *fakeAssemblyWithCells) ID() string { return a.id }
func (a *fakeAssemblyWithCells) CellIDs() []string {
	ids := make([]string, 0, len(a.cells))
	for id := range a.cells {
		ids = append(ids, id)
	}
	return ids
}
func (a *fakeAssemblyWithCells) Cell(id string) cell.Cell { return a.cells[id] }

func TestRunAuthPlanValidateHooks_DiscoverScenarios(t *testing.T) {
	t.Parallel()

	verifier := &applyStubVerifier{}

	t.Run("zero_providers_returns_error", func(t *testing.T) {
		t.Parallel()
		asm := &fakeAssemblyWithCells{id: "no-providers", cells: map[string]cell.Cell{}}
		b := newMinimalBootstrap()
		b.listenerConfigs[cell.PrimaryListener] = listenerConfig{
			authChain: []cell.ListenerAuth{cell.NewAuthJWTFromAssembly(asm)},
		}
		err := b.runAuthPlanValidateHooks()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "no cell.AuthProvider")
	})

	t.Run("multiple_providers_returns_error", func(t *testing.T) {
		t.Parallel()
		asm := &fakeAssemblyWithCells{
			id: "two-providers",
			cells: map[string]cell.Cell{
				"cell-a": newFakeAuthCell("cell-a", verifier),
				"cell-b": newFakeAuthCell("cell-b", verifier),
			},
		}
		b := newMinimalBootstrap()
		b.listenerConfigs[cell.PrimaryListener] = listenerConfig{
			authChain: []cell.ListenerAuth{cell.NewAuthJWTFromAssembly(asm)},
		}
		err := b.runAuthPlanValidateHooks()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "multiple cell.AuthProvider")
	})

	t.Run("nil_verifier_returns_error", func(t *testing.T) {
		t.Parallel()
		asm := &fakeAssemblyWithCells{
			id: "nil-verifier-asm",
			cells: map[string]cell.Cell{
				"cell-nil": newFakeAuthCell("cell-nil", nil),
			},
		}
		b := newMinimalBootstrap()
		b.listenerConfigs[cell.PrimaryListener] = listenerConfig{
			authChain: []cell.ListenerAuth{cell.NewAuthJWTFromAssembly(asm)},
		}
		err := b.runAuthPlanValidateHooks()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "TokenVerifier() returned nil")
	})

	t.Run("single_provider_resolves_verifier", func(t *testing.T) {
		t.Parallel()
		asm := &fakeAssemblyWithCells{
			id: "single-provider",
			cells: map[string]cell.Cell{
				"cell-auth": newFakeAuthCell("cell-auth", verifier),
			},
		}
		plan := cell.NewAuthJWTFromAssembly(asm)
		b := newMinimalBootstrap()
		b.listenerConfigs[cell.PrimaryListener] = listenerConfig{
			authChain: []cell.ListenerAuth{plan},
		}
		err := b.runAuthPlanValidateHooks()
		require.NoError(t, err)
		// Retrieve the plan back from the config (p.SetResolved writes via atomic).
		cfg := b.listenerConfigs[cell.PrimaryListener]
		resolved, ok := cfg.authChain[0].(cell.AuthJWTFromAssembly)
		require.True(t, ok)
		assert.NotNil(t, resolved.ResolvedVerifier())
	})
}

// ─── TestSortedListenerRefs ───────────────────────────────────────────────────

func TestSortedListenerRefs_Deterministic(t *testing.T) {
	t.Parallel()

	configs := map[cell.ListenerRef]listenerConfig{
		cell.HealthListener:   {},
		cell.PrimaryListener:  {},
		cell.InternalListener: {},
	}
	refs := sortedListenerRefs(configs)
	require.Len(t, refs, 3)
	// Verify sorted order.
	for i := 1; i < len(refs); i++ {
		assert.Less(t, refs[i-1].String(), refs[i].String(),
			"refs should be in ascending string order")
	}
}

// ─── TestExplicitAuthNone ─────────────────────────────────────────────────────

func TestExplicitAuthNone(t *testing.T) {
	t.Parallel()

	verifier := &applyStubVerifier{}
	tests := []struct {
		name  string
		chain []cell.ListenerAuth
		want  bool
	}{
		{"nil_chain", nil, false},
		{"empty_chain", []cell.ListenerAuth{}, false},
		{"auth_none_explicit", []cell.ListenerAuth{cell.AuthNone{}}, true},
		{"jwt_plan", []cell.ListenerAuth{cell.NewAuthJWT(verifier)}, false},
		{"mtls_plan", []cell.ListenerAuth{cell.AuthMTLS{}}, false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, explicitAuthNone(tc.chain))
		})
	}
}
