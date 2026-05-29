package bootstrap

// auth_plan_apply_test.go — white-box table-driven tests for applyListenerAuthChain,
// mtlsMiddleware, and runAuthPlanValidateHooks (phase4 discovery).
// Uses package bootstrap (not bootstrap_test) for white-box access to unexported helpers.

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"

	kauth "github.com/ghbvf/gocell/kernel/auth"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/auth/authtest"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/runtime/auth"
	routerpkg "github.com/ghbvf/gocell/runtime/http/router"
)

// ─── Stubs ────────────────────────────────────────────────────────────────────

// applyStubVerifier satisfies kauth.IntentTokenVerifier / kauth.IntentTokenVerifier.
type applyStubVerifier struct{}

func (v *applyStubVerifier) VerifyIntent(_ context.Context, _ string, _ kauth.TokenIntent) (kauth.Claims, error) {
	return kauth.Claims{}, nil
}

// applyStubNonceStore satisfies kauth.NonceStore.
type applyStubNonceStore struct{}

func (s *applyStubNonceStore) CheckAndMark(_ context.Context, _ string) error { return nil }
func (s *applyStubNonceStore) Kind() kauth.NonceStoreKind                     { return kauth.NonceStoreKindInMemory }

type applyNoopNonceStore struct{}

func (s *applyNoopNonceStore) CheckAndMark(_ context.Context, _ string) error { return nil }
func (s *applyNoopNonceStore) Kind() kauth.NonceStoreKind                     { return kauth.NonceStoreKindNoop }

// applyStubHMACKeyring satisfies kauth.HMACKeyring.
type applyStubHMACKeyring struct{}

func (k *applyStubHMACKeyring) Current() []byte   { return []byte("secret-32-bytes-padding-here----") }
func (k *applyStubHMACKeyring) Secrets() [][]byte { return [][]byte{k.Current()} }

// Compile-time guard: kauth.AssemblyRef must expose Cell(id string) cell.Cell
// so that runtime/bootstrap can resolve registered cells by ID without an
// implicit type assertion to a private sub-interface. The canonical guard
// is the AST-level ASSEMBLYREF-METHOD-SET-01 archtest
// (tools/archtest/assemblyref_method_set_test.go); this method-expression
// reference adds a typecheck-time tripwire that fails this test binary's
// build immediately if Cell is removed from AssemblyRef.
var _ func(kauth.AssemblyRef, string) cell.Cell = kauth.AssemblyRef.Cell

// applyStubAssemblyRef satisfies kauth.AssemblyRef. Cell always returns nil
// because the tests using this stub validate auth-chain placement and
// singleton invariants — they do not exercise authProvider discovery, which
// has dedicated coverage via fakeAssemblyWithCells below.
type applyStubAssemblyRef struct {
	id      string
	cellIDs []string
}

func (a *applyStubAssemblyRef) ID() string              { return a.id }
func (a *applyStubAssemblyRef) CellIDs() []string       { return a.cellIDs }
func (a *applyStubAssemblyRef) Cell(_ string) cell.Cell { return nil }

// ─── Helpers ──────────────────────────────────────────────────────────────────

// newMinimalBootstrap creates a Bootstrap with no assembly for use in apply tests.
func newMinimalBootstrap() *Bootstrap {
	return &Bootstrap{
		listenerConfigs: make(map[cell.ListenerRef]listenerConfig),
		clock:           clock.Real(),
	}
}

// routerInstallsAuthMiddleware applies router options to a real Router and
// observes request behavior. A protected route returns 401 without an
// Authorization header only when router.WithAuthMiddleware is actually wired.
func routerInstallsAuthMiddleware(t *testing.T, opts []routerpkg.Option) bool {
	t.Helper()

	rtr, err := routerpkg.NewForListener(clock.Real(), cell.PrimaryListener, opts...)
	require.NoError(t, err)

	require.NoError(t, auth.Mount(rtr, auth.Route{
		Contract: testHTTPContract(http.MethodGet, "/auth-plan/protected"),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}),
	}))
	require.NoError(t, rtr.FinalizeAuth())

	rec := httptest.NewRecorder()
	rtr.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/auth-plan/protected", nil))

	switch rec.Code {
	case http.StatusUnauthorized:
		return true
	case http.StatusOK:
		return false
	default:
		t.Fatalf("protected route returned status %d, want 401 with auth middleware or 200 without it", rec.Code)
		return false
	}
}

// ─── TestApplyListenerAuthChain_EachKind ──────────────────────────────────────

func TestApplyListenerAuthChain_EachKind(t *testing.T) {
	t.Parallel()

	verifier := &applyStubVerifier{}
	store := &applyStubNonceStore{}
	ring := &applyStubHMACKeyring{}
	asm := &applyStubAssemblyRef{id: "test-asm"}

	resolvedPlan := authtest.MustAuthJWTFromAssembly(asm)
	resolvedPlan.SetResolved(verifier)

	ref := cell.PrimaryListener

	tests := []struct {
		name              string
		chain             []kauth.ListenerAuth
		wantMWCount       int
		wantAuthInstalled bool
		wantDescribe      string
		wantErr           bool
	}{
		{
			name:              "AuthNone",
			chain:             []kauth.ListenerAuth{kauth.AuthNone{}},
			wantMWCount:       0,
			wantAuthInstalled: false,
			wantDescribe:      "none",
		},
		{
			name:              "AuthJWT",
			chain:             []kauth.ListenerAuth{authtest.MustAuthJWT(verifier)},
			wantMWCount:       0,
			wantAuthInstalled: true,
			wantDescribe:      "jwt",
		},
		{
			name:              "AuthJWTFromAssembly_resolved",
			chain:             []kauth.ListenerAuth{resolvedPlan},
			wantMWCount:       0,
			wantAuthInstalled: true,
			wantDescribe:      "jwt",
		},
		{
			name: "AuthJWTFromAssembly_unresolved",
			chain: []kauth.ListenerAuth{
				authtest.MustAuthJWTFromAssembly(asm), // not SetResolved
			},
			wantErr: true,
		},
		{
			name:              "AuthMTLS",
			chain:             []kauth.ListenerAuth{kauth.AuthMTLS{}},
			wantMWCount:       1,
			wantAuthInstalled: false,
			wantDescribe:      "mtls",
		},
		{
			name:              "AuthServiceToken",
			chain:             []kauth.ListenerAuth{authtest.MustAuthServiceToken(store, ring)},
			wantMWCount:       1,
			wantAuthInstalled: false,
			wantDescribe:      "service-token",
		},
		{
			name: "MultiPlan_MTLSAndServiceToken",
			chain: []kauth.ListenerAuth{
				kauth.AuthMTLS{},
				authtest.MustAuthServiceToken(store, ring),
			},
			wantMWCount:       2,
			wantAuthInstalled: false,
			wantDescribe:      "mtls+service-token",
		},
	}

	for _, tc := range tests {
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
			assert.Equal(t, tc.wantAuthInstalled, routerInstallsAuthMiddleware(t, routerOpts),
				"auth middleware installation")
		})
	}
}

// ─── TestVerboseTokenMiddleware_QueryParamBoundary ────────────────────────────

// ─── TestRunAuthPlanValidateHooks_DiscoverScenarios ───────────────────────────

// fakeAuthProviderCell implements cell.Cell (via embedded BaseCell) and
// kauth.AuthProvider so it can be used as a fake auth-provider cell in
// runAuthPlanValidateHooks tests.
type fakeAuthProviderCell struct {
	*cell.BaseCell
	verifier kauth.IntentTokenVerifier
}

func newFakeAuthCell(id string, v kauth.IntentTokenVerifier) *fakeAuthProviderCell {
	base := cell.MustNewBaseCell(&metadata.CellMeta{
		ID:               id,
		Type:             "core",
		ConsistencyLevel: "L1",
	})
	return &fakeAuthProviderCell{BaseCell: base, verifier: v}
}

func (c *fakeAuthProviderCell) TokenVerifier() kauth.IntentTokenVerifier { return c.verifier }

// Ensure fakeAuthProviderCell satisfies kauth.AuthProvider at compile time.
var _ kauth.AuthProvider = (*fakeAuthProviderCell)(nil)

// fakeAssemblyWithCells satisfies kauth.AssemblyRef with an in-memory cell map,
// providing the by-ID lookup that authProvider discovery exercises.
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
	// Sort to match production CoreAssembly.CellIDs ordering, so that
	// discovery-error messages naming foundID/id are deterministic.
	sort.Strings(ids)
	return ids
}
func (a *fakeAssemblyWithCells) Cell(id string) cell.Cell { return a.cells[id] }

func TestRunAuthPlanValidateHooks_DiscoverScenarios(t *testing.T) {
	t.Parallel()

	verifier := &applyStubVerifier{}

	t.Run("typed_nil_assembly_returns_error", func(t *testing.T) {
		t.Parallel()
		var asm *applyStubAssemblyRef
		var err error

		require.NotPanics(t, func() {
			_, err = discoverAuthVerifierFromAssembly(asm)
		})
		require.Error(t, err)
		assert.Contains(t, errFull(t, err), "Assembly is nil")
	})

	t.Run("zero_providers_returns_error", func(t *testing.T) {
		t.Parallel()
		asm := &fakeAssemblyWithCells{id: "no-providers", cells: map[string]cell.Cell{}}
		b := newMinimalBootstrap()
		b.listenerConfigs[cell.PrimaryListener] = listenerConfig{
			authChain: []kauth.ListenerAuth{authtest.MustAuthJWTFromAssembly(asm)},
		}
		err := b.runAuthPlanValidateHooks()
		require.Error(t, err)
		assert.Contains(t, errFull(t, err), "authProvider cell")
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
			authChain: []kauth.ListenerAuth{authtest.MustAuthJWTFromAssembly(asm)},
		}
		err := b.runAuthPlanValidateHooks()
		require.Error(t, err)
		assert.Contains(t, errFull(t, err), "multiple authProvider cells")
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
			authChain: []kauth.ListenerAuth{authtest.MustAuthJWTFromAssembly(asm)},
		}
		err := b.runAuthPlanValidateHooks()
		require.Error(t, err)
		assert.Contains(t, errFull(t, err), "TokenVerifier() returned nil")
	})

	t.Run("single_provider_resolves_verifier", func(t *testing.T) {
		t.Parallel()
		asm := &fakeAssemblyWithCells{
			id: "single-provider",
			cells: map[string]cell.Cell{
				"cell-auth": newFakeAuthCell("cell-auth", verifier),
			},
		}
		plan := authtest.MustAuthJWTFromAssembly(asm)
		b := newMinimalBootstrap()
		b.listenerConfigs[cell.PrimaryListener] = listenerConfig{
			authChain: []kauth.ListenerAuth{plan},
		}
		err := b.runAuthPlanValidateHooks()
		require.NoError(t, err)
		// Retrieve the plan back from the config (p.SetResolved writes via atomic).
		cfg := b.listenerConfigs[cell.PrimaryListener]
		resolved, ok := cfg.authChain[0].(kauth.AuthJWTFromAssembly)
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
		chain []kauth.ListenerAuth
		want  bool
	}{
		{"nil_chain", nil, false},
		{"empty_chain", []kauth.ListenerAuth{}, false},
		{"auth_none_explicit", []kauth.ListenerAuth{kauth.AuthNone{}}, true},
		{"jwt_plan", []kauth.ListenerAuth{authtest.MustAuthJWT(verifier)}, false},
		{"mtls_plan", []kauth.ListenerAuth{kauth.AuthMTLS{}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, explicitAuthNone(tc.chain))
		})
	}
}
