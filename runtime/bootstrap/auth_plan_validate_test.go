package bootstrap

// auth_plan_validate_test.go — white-box table-driven tests for
// validateAuthChainJWTSingleton, validateAuthJWTFromAssemblyPlans, and
// validateAuthPlanMTLSBindings. Uses package bootstrap for white-box access.

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"testing"

	"github.com/ghbvf/gocell/kernel/auth"
	"github.com/ghbvf/gocell/kernel/outbox"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/auth/authtest"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
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

func TestValidateAuthServiceTokenPlans(t *testing.T) {
	t.Parallel()

	validPlan := authtest.MustAuthServiceToken(&applyStubNonceStore{}, &applyStubHMACKeyring{})

	tests := []struct {
		name    string
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
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			b := bootstrapWithListener(cell.InternalListener, tc.chain, nil)

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
