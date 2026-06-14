package bootstrap

// auth_plan_validate_test.go — white-box table-driven tests for
// validateAuthChainJWTSingleton, validateAuthJWTFromAssemblyPlans, and
// validateAuthPlanMTLSBindings. Uses package bootstrap for white-box access.

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/outbox"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/framework/kernel/assembly"
	"github.com/ghbvf/gocell/framework/kernel/auth/authtest"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// errFull returns Message + " " + Error() for *errcode.Error (Error() includes
// internal diagnostic text), falling back to err.Error() for other error types.
func errFull(t *testing.T, err error) string {
	t.Helper()
	var ecErr *errcode.Error
	if errors.As(err, &ecErr) {
		return ecErr.Message + " " + ecErr.Error()
	}
	return err.Error()
}

// ─── Helpers ──────────────────────────────────────────────────────────────────

// bootstrapWithListener creates a minimal Bootstrap with a single listener.
func bootstrapWithListener(ref cell.ListenerRef, chain []auth.ListenerAuth, tlsCfg *tls.Config) *Bootstrap {
	b := &Bootstrap{
		listenerConfigs: map[cell.ListenerRef]listenerConfig{
			ref: {
				ref:       ref,
				addr:      "127.0.0.1:0",
				authChain: chain,
				tls:       tlsCfg,
			},
		},
	}
	return b
}

// validMTLSTLSConfig returns a *tls.Config that passes validateMTLSTLSConfig.
func validMTLSTLSConfig() *tls.Config {
	return &tls.Config{
		ClientAuth: tls.RequireAndVerifyClientCert,
		ClientCAs:  x509.NewCertPool(),
	}
}

// ─── TestValidateAuthChainJWTSingleton ────────────────────────────────────────

func TestValidateAuthChainJWTSingleton(t *testing.T) {
	t.Parallel()

	verifier := &applyStubVerifier{}
	asm := &applyStubAssemblyRef{id: "asm-singleton"}

	tests := []struct {
		name    string
		chain   []auth.ListenerAuth
		wantErr bool
		errMsg  string
	}{
		{
			name:    "AcceptsJWTFirst",
			chain:   []auth.ListenerAuth{authtest.MustAuthJWT(verifier), auth.AuthMTLS{}},
			wantErr: false,
		},
		{
			name: "AcceptsJWTFromAssemblyFirst",
			chain: []auth.ListenerAuth{
				authtest.MustAuthJWTFromAssembly(asm),
				authtest.MustAuthServiceToken(&applyStubNonceStore{}, &applyStubHMACKeyring{}),
			},
			wantErr: false,
		},
		{
			name:    "AcceptsJWTAlone",
			chain:   []auth.ListenerAuth{authtest.MustAuthJWT(verifier)},
			wantErr: false,
		},
		{
			name:    "RejectsJWTNotFirst",
			chain:   []auth.ListenerAuth{auth.AuthMTLS{}, authtest.MustAuthJWT(verifier)},
			wantErr: true,
			errMsg:  "must be sole/first plan",
		},
		{
			name:    "RejectsDuplicateJWT",
			chain:   []auth.ListenerAuth{authtest.MustAuthJWT(verifier), authtest.MustAuthJWT(verifier)},
			wantErr: true,
			errMsg:  "at most one",
		},
		{
			name:    "AcceptsNoJWT",
			chain:   []auth.ListenerAuth{auth.AuthMTLS{}},
			wantErr: false,
		},
		{
			name:    "AcceptsEmptyChain",
			chain:   nil,
			wantErr: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := bootstrapWithListener(cell.PrimaryListener, tc.chain, nil)
			err := b.validateAuthChainJWTSingleton()
			if tc.wantErr {
				require.Error(t, err)
				if tc.errMsg != "" {
					assert.Contains(t, errFull(t, err), tc.errMsg)
				}
				// Also verify the error contains the listener ref.
				assert.Contains(t, errFull(t, err), cell.PrimaryListener.String())
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// ─── TestValidateAuthJWTFromAssemblyPlans ─────────────────────────────────────

func TestValidateAuthJWTFromAssemblyPlans(t *testing.T) {
	t.Parallel()

	asmA := assembly.New(clock.Real(), assembly.Config{ID: "asm-match-a", DurabilityMode: outbox.DurabilityDemo})
	asmB := assembly.New(clock.Real(), assembly.Config{ID: "asm-match-b", DurabilityMode: outbox.DurabilityDemo})

	t.Run("Match_SameInstance", func(t *testing.T) {
		t.Parallel()
		b := New(
			clock.Real(),
			WithAssembly(asmA),
			WithListener(cell.PrimaryListener, "127.0.0.1:0",
				[]auth.ListenerAuth{authtest.MustAuthJWTFromAssembly(asmA)}),
		)
		err := b.validateAuthJWTFromAssemblyPlans()
		require.NoError(t, err)
	})

	t.Run("Mismatch_DifferentInstances", func(t *testing.T) {
		t.Parallel()
		b := New(
			clock.Real(),
			WithAssembly(asmA),
			WithListener(cell.PrimaryListener, "127.0.0.1:0",
				[]auth.ListenerAuth{authtest.MustAuthJWTFromAssembly(asmB)}),
		)
		err := b.validateAuthJWTFromAssemblyPlans()
		require.Error(t, err)
		assert.Contains(t, errFull(t, err), "AuthJWTFromAssembly")
		assert.Contains(t, errFull(t, err), "asm-match-a")
		assert.Contains(t, errFull(t, err), "asm-match-b")
		assert.Contains(t, errFull(t, err), cell.PrimaryListener.String())
	})

	t.Run("Mismatch_SameIDDifferentInstances", func(t *testing.T) {
		t.Parallel()
		asmWithID := assembly.New(clock.Real(), assembly.Config{ID: "asm-match-same-id", DurabilityMode: outbox.DurabilityDemo})
		otherWithSameID := assembly.New(clock.Real(), assembly.Config{ID: "asm-match-same-id", DurabilityMode: outbox.DurabilityDemo})
		b := New(
			clock.Real(),
			WithAssembly(asmWithID),
			WithListener(cell.PrimaryListener, "127.0.0.1:0",
				[]auth.ListenerAuth{authtest.MustAuthJWTFromAssembly(otherWithSameID)}),
		)

		err := b.validateAuthJWTFromAssemblyPlans()
		require.Error(t, err)
		assert.Contains(t, errFull(t, err), "AuthJWTFromAssembly")
		assert.Contains(t, errFull(t, err), "same *assembly.CoreAssembly instance")
		assert.Contains(t, errFull(t, err), cell.PrimaryListener.String())
	})

	t.Run("NilAssembly_NoError", func(t *testing.T) {
		t.Parallel()
		// No WithAssembly — b.assembly is nil; validateAuthJWTFromAssemblyPlans
		// should return nil immediately.
		b := &Bootstrap{
			listenerConfigs: map[cell.ListenerRef]listenerConfig{
				cell.PrimaryListener: {
					authChain: []auth.ListenerAuth{auth.AuthMTLS{}},
				},
			},
		}
		require.NoError(t, b.validateAuthJWTFromAssemblyPlans())
	})

	t.Run("ConstructedPlanWithoutWithAssembly_NoError", func(t *testing.T) {
		t.Parallel()
		asm := &applyStubAssemblyRef{id: "valid-no-withassembly"}
		b := bootstrapWithListener(
			cell.PrimaryListener,
			[]auth.ListenerAuth{authtest.MustAuthJWTFromAssembly(asm)},
			nil,
		)
		require.NoError(t, b.validateAuthJWTFromAssemblyPlans())
	})
}

func TestValidateAuthJWTFromAssemblyPlans_RejectsConstructorBypass(t *testing.T) {
	t.Parallel()

	asm := &applyStubAssemblyRef{id: "literal-asm"}
	var typedNil *applyStubAssemblyRef

	tests := []struct {
		name    string
		plan    auth.AuthJWTFromAssembly
		wantErr string
	}{
		{
			name:    "zero literal",
			plan:    auth.AuthJWTFromAssembly{},
			wantErr: "Assembly must not be nil",
		},
		{
			name:    "nil Assembly literal",
			plan:    auth.AuthJWTFromAssembly{Assembly: nil},
			wantErr: "Assembly must not be nil",
		},
		{
			name:    "typed nil Assembly literal",
			plan:    auth.AuthJWTFromAssembly{Assembly: typedNil},
			wantErr: "Assembly must not be nil",
		},
		{
			name:    "real Assembly literal without resolver",
			plan:    auth.AuthJWTFromAssembly{Assembly: asm},
			wantErr: "constructed as a struct literal",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := bootstrapWithListener(cell.PrimaryListener, []auth.ListenerAuth{tc.plan}, nil)

			var err error
			require.NotPanics(t, func() {
				err = b.validateAuthJWTFromAssemblyPlans()
			})
			require.Error(t, err)
			assert.Contains(t, errFull(t, err), tc.wantErr)
			assert.Contains(t, errFull(t, err), cell.PrimaryListener.String())
		})
	}
}

// ─── TestValidateAuthPlanMTLSBindings ─────────────────────────────────────────

func TestValidateAuthPlanMTLSBindings(t *testing.T) {
	t.Parallel()

	t.Run("ListenerPath_NoTLSConfig", func(t *testing.T) {
		t.Parallel()
		b := bootstrapWithListener(
			cell.InternalListener,
			[]auth.ListenerAuth{auth.AuthMTLS{}},
			nil, // no tls.Config
		)
		err := b.validateAuthPlanMTLSBindings()
		require.Error(t, err)
		assert.Contains(t, errFull(t, err), "AuthMTLS")
		assert.Contains(t, errFull(t, err), cell.InternalListener.String())
	})

	t.Run("ListenerPath_LooseClientAuth", func(t *testing.T) {
		t.Parallel()
		b := bootstrapWithListener(
			cell.InternalListener,
			[]auth.ListenerAuth{auth.AuthMTLS{}},
			&tls.Config{
				ClientAuth: tls.NoClientCert, // too loose
				ClientCAs:  x509.NewCertPool(),
			},
		)
		err := b.validateAuthPlanMTLSBindings()
		require.Error(t, err)
		assert.Contains(t, errFull(t, err), "ClientAuth")
	})

	t.Run("ListenerPath_NoClientCAs", func(t *testing.T) {
		t.Parallel()
		b := bootstrapWithListener(
			cell.InternalListener,
			[]auth.ListenerAuth{auth.AuthMTLS{}},
			&tls.Config{
				ClientAuth: tls.RequireAndVerifyClientCert,
				ClientCAs:  nil, // missing
			},
		)
		err := b.validateAuthPlanMTLSBindings()
		require.Error(t, err)
		assert.Contains(t, errFull(t, err), "ClientCAs")
	})

	t.Run("ListenerPath_AllValid", func(t *testing.T) {
		t.Parallel()
		b := bootstrapWithListener(
			cell.InternalListener,
			[]auth.ListenerAuth{auth.AuthMTLS{}},
			validMTLSTLSConfig(),
		)
		require.NoError(t, b.validateAuthPlanMTLSBindings())
	})
	// PR269 round-3: RouteGroupPath_* subtests removed — RouteGroup.Auth no
	// longer exists; mTLS bindings are validated only at listener scope.
}

func TestValidateAuthNoneExclusive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		chain   []auth.ListenerAuth
		wantErr bool
	}{
		{name: "AuthNone alone accepted", chain: []auth.ListenerAuth{auth.AuthNone{}}},
		{name: "guard alone accepted", chain: []auth.ListenerAuth{auth.AuthMTLS{}}},
		{name: "AuthNone mixed with guard rejected", chain: []auth.ListenerAuth{auth.AuthNone{}, auth.AuthMTLS{}}, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := bootstrapWithListener(cell.PrimaryListener, tc.chain, nil)

			err := b.validateAuthNoneExclusive()
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, errFull(t, err), "AuthNone cannot be mixed")
			assert.Contains(t, errFull(t, err), cell.PrimaryListener.String())
		})
	}
}

func TestValidateAuthOperatorPlans(t *testing.T) {
	t.Parallel()

	validOp := newTestOperatorAuth(t)

	tests := []struct {
		name       string
		ref        cell.ListenerRef
		chain      []auth.ListenerAuth
		wantErr    bool
		wantErrSub string
	}{
		{
			name:  "operator on AdminListener accepted",
			ref:   cell.AdminListener,
			chain: []auth.ListenerAuth{validOp},
		},
		{
			name:       "operator on PrimaryListener rejected",
			ref:        cell.PrimaryListener,
			chain:      []auth.ListenerAuth{validOp},
			wantErr:    true,
			wantErrSub: "only be used on cell.AdminListener",
		},
		{
			name:       "operator on InternalListener rejected",
			ref:        cell.InternalListener,
			chain:      []auth.ListenerAuth{validOp},
			wantErr:    true,
			wantErrSub: "only be used on cell.AdminListener",
		},
		{
			name:       "AdminListener without operator rejected",
			ref:        cell.AdminListener,
			chain:      []auth.ListenerAuth{auth.AuthNone{}},
			wantErr:    true,
			wantErrSub: "requires an AuthOperator",
		},
		{
			name:       "struct-literal empty credentials rejected",
			ref:        cell.AdminListener,
			chain:      []auth.ListenerAuth{auth.AuthOperator{Limiter: allowAllLimiter{}}},
			wantErr:    true,
			wantErrSub: "non-empty operator credentials",
		},
		{
			name:       "struct-literal nil limiter rejected",
			ref:        cell.AdminListener,
			chain:      []auth.ListenerAuth{auth.AuthOperator{Username: []byte("ops"), Password: []byte("pw")}},
			wantErr:    true,
			wantErrSub: "Limiter must not be nil",
		},
		{
			name:       "two operators rejected",
			ref:        cell.AdminListener,
			chain:      []auth.ListenerAuth{validOp, validOp},
			wantErr:    true,
			wantErrSub: "at most one AuthOperator",
		},
		{
			name:  "non-admin listener without operator is fine",
			ref:   cell.PrimaryListener,
			chain: []auth.ListenerAuth{auth.AuthMTLS{}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := bootstrapWithListener(tc.ref, tc.chain, nil)
			err := b.validateAuthOperatorPlans()
			if !tc.wantErr {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, errFull(t, err), tc.wantErrSub)
		})
	}
}

// applyDistributedNonceStore reports NonceStoreKindDistributed (the cross-pod
// replay-safe kind). Used to exercise the multi-pod accept path.
type applyDistributedNonceStore struct{}

func (s *applyDistributedNonceStore) CheckAndMark(_ context.Context, _ string) error { return nil }
func (s *applyDistributedNonceStore) Kind() auth.NonceStoreKind {
	return auth.NonceStoreKindDistributed
}

// applyUnknownNonceStore reports an unrecognized kind, exercising the fail-closed
// default branch of the replay-safe check (#1410 review F1/F2).
type applyUnknownNonceStore struct{}

func (s *applyUnknownNonceStore) CheckAndMark(_ context.Context, _ string) error { return nil }
func (s *applyUnknownNonceStore) Kind() auth.NonceStoreKind {
	return auth.NonceStoreKind("totally-bogus-kind")
}

// realTopo builds a real adapter-mode topology with the given single-pod flag.
func realTopo(t *testing.T, singlePod bool) Topology {
	t.Helper()
	topo, err := NewTopology("real", "postgres", singlePod)
	require.NoError(t, err)
	return topo
}

func TestValidateAuthServiceTokenPlans(t *testing.T) {
	t.Parallel()

	validPlan := authtest.MustAuthServiceToken(&applyStubNonceStore{}, &applyStubHMACKeyring{})
	distributedPlan := authtest.MustAuthServiceToken(&applyDistributedNonceStore{}, &applyStubHMACKeyring{})
	unknownPlan := authtest.MustAuthServiceToken(&applyUnknownNonceStore{}, &applyStubHMACKeyring{})

	tests := []struct {
		name string
		// topo is the control-plane topology injected via WithControlPlaneTopology.
		// The zero value (dev mode) skips the topology-dependent replay check, so
		// existing cases keep their prior behavior.
		topo    Topology
		chain   []auth.ListenerAuth
		wantErr string
	}{
		{
			name:  "accepts one service token",
			chain: []auth.ListenerAuth{validPlan},
		},
		{
			name:  "accepts mtls plus one service token",
			chain: []auth.ListenerAuth{auth.AuthMTLS{}, validPlan},
		},
		{
			name: "rejects duplicate service token",
			chain: []auth.ListenerAuth{
				validPlan,
				validPlan,
			},
			wantErr: "at most one AuthServiceToken",
		},
		{
			name: "rejects nil nonce store",
			chain: []auth.ListenerAuth{
				auth.AuthServiceToken{Store: nil, Ring: &applyStubHMACKeyring{}},
			},
			wantErr: "Store must not be nil",
		},
		{
			name: "rejects nil keyring",
			chain: []auth.ListenerAuth{
				auth.AuthServiceToken{Store: &applyStubNonceStore{}, Ring: nil},
			},
			wantErr: "Ring must not be nil",
		},
		{
			name: "rejects noop nonce store literal",
			chain: []auth.ListenerAuth{
				auth.AuthServiceToken{Store: &applyNoopNonceStore{}, Ring: &applyStubHMACKeyring{}},
			},
			wantErr: "NonceStoreKindNoop",
		},
		// --- #1410 review F1: topology-dependent replay-safety on the ACTUAL plan store ---
		{
			name:    "real multi-pod rejects in-memory store (F1 loop closed)",
			topo:    realTopo(t, false),
			chain:   []auth.ListenerAuth{validPlan}, // applyStubNonceStore == in_memory
			wantErr: "not replay-safe",
		},
		{
			name:  "real single-pod accepts in-memory store",
			topo:  realTopo(t, true),
			chain: []auth.ListenerAuth{validPlan},
		},
		{
			name:  "real multi-pod accepts distributed store",
			topo:  realTopo(t, false),
			chain: []auth.ListenerAuth{distributedPlan},
		},
		{
			name:    "real multi-pod rejects unknown kind fail-closed",
			topo:    realTopo(t, false),
			chain:   []auth.ListenerAuth{unknownPlan},
			wantErr: "not replay-safe",
		},
		{
			name:    "real single-pod rejects unknown kind fail-closed",
			topo:    realTopo(t, true),
			chain:   []auth.ListenerAuth{unknownPlan},
			wantErr: "not replay-safe",
		},
		{
			// Zero Topology is dev mode (RequireProductionControlPlane is false),
			// so the topology-dependent replay check is skipped and in-memory passes.
			name:  "dev mode skips replay check (in-memory accepted)",
			topo:  Topology{},
			chain: []auth.ListenerAuth{validPlan},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := bootstrapWithListener(cell.InternalListener, tc.chain, nil)
			b.controlPlaneTopology = tc.topo

			err := b.validateAuthServiceTokenPlans()
			if tc.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, errFull(t, err), tc.wantErr)
			assert.Contains(t, errFull(t, err), cell.InternalListener.String())
		})
	}
}

// ─── TestCheckJWTSingleton (unit test of inner helper) ───────────────────────

func TestCheckJWTSingleton(t *testing.T) {
	t.Parallel()

	verifier := &applyStubVerifier{}
	asm := &applyStubAssemblyRef{id: "asm-check"}

	tests := []struct {
		name    string
		chain   []auth.ListenerAuth
		wantErr bool
		errMsg  string
	}{
		{"empty", nil, false, ""},
		{"jwt_alone", []auth.ListenerAuth{authtest.MustAuthJWT(verifier)}, false, ""},
		{"jwt_from_assembly_alone", []auth.ListenerAuth{authtest.MustAuthJWTFromAssembly(asm)}, false, ""},
		{
			"jwt_not_first",
			[]auth.ListenerAuth{auth.AuthMTLS{}, authtest.MustAuthJWT(verifier)},
			true, "sole/first",
		},
		{
			"dual_jwt",
			[]auth.ListenerAuth{authtest.MustAuthJWT(verifier), authtest.MustAuthJWT(verifier)},
			true, "at most one",
		},
		{
			"jwt_and_jwt_from_assembly",
			[]auth.ListenerAuth{authtest.MustAuthJWT(verifier), authtest.MustAuthJWTFromAssembly(asm)},
			true, "at most one",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			err := checkJWTSingleton("test-listener", tc.chain)
			if tc.wantErr {
				require.Error(t, err)
				if tc.errMsg != "" {
					assert.Contains(t, errFull(t, err), tc.errMsg)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}
