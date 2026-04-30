package bootstrap

// auth_plan_validate_test.go — white-box table-driven tests for
// validateAuthChainJWTSingleton, validateAuthPlanAssemblyMatch, and
// validateAuthPlanMTLSBindings. Uses package bootstrap for white-box access.

import (
	"crypto/tls"
	"crypto/x509"
	"testing"

	"github.com/ghbvf/gocell/kernel/assembly"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ─── Helpers ──────────────────────────────────────────────────────────────────

// bootstrapWithListener creates a minimal Bootstrap with a single listener.
func bootstrapWithListener(ref cell.ListenerRef, chain []cell.ListenerAuth, tlsCfg *tls.Config) *Bootstrap {
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
		chain   []cell.ListenerAuth
		wantErr bool
		errMsg  string
	}{
		{
			name:    "AcceptsJWTFirst",
			chain:   []cell.ListenerAuth{cell.MustNewAuthJWT(verifier), cell.AuthMTLS{}},
			wantErr: false,
		},
		{
			name:    "AcceptsJWTFromAssemblyFirst",
			chain:   []cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm), cell.MustNewAuthServiceToken(&applyStubNonceStore{}, &applyStubHMACKeyring{})},
			wantErr: false,
		},
		{
			name:    "AcceptsJWTAlone",
			chain:   []cell.ListenerAuth{cell.MustNewAuthJWT(verifier)},
			wantErr: false,
		},
		{
			name:    "RejectsJWTNotFirst",
			chain:   []cell.ListenerAuth{cell.AuthMTLS{}, cell.MustNewAuthJWT(verifier)},
			wantErr: true,
			errMsg:  "must be sole/first plan",
		},
		{
			name:    "RejectsDuplicateJWT",
			chain:   []cell.ListenerAuth{cell.MustNewAuthJWT(verifier), cell.MustNewAuthJWT(verifier)},
			wantErr: true,
			errMsg:  "at most one",
		},
		{
			name:    "AcceptsNoJWT",
			chain:   []cell.ListenerAuth{cell.AuthMTLS{}},
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
					assert.Contains(t, err.Error(), tc.errMsg)
				}
				// Also verify the error contains the listener ref.
				assert.Contains(t, err.Error(), cell.PrimaryListener.String())
			} else {
				require.NoError(t, err)
			}
		})
	}
}

// ─── TestValidateAuthPlanAssemblyMatch ────────────────────────────────────────

func TestValidateAuthPlanAssemblyMatch(t *testing.T) {
	t.Parallel()

	asmA := assembly.New(assembly.Config{ID: "asm-match-a", DurabilityMode: cell.DurabilityDemo})
	asmB := assembly.New(assembly.Config{ID: "asm-match-b", DurabilityMode: cell.DurabilityDemo})

	t.Run("Match_SameInstance", func(t *testing.T) {
		t.Parallel()
		b := New(
			WithAssembly(asmA),
			WithListener(cell.PrimaryListener, "127.0.0.1:0",
				[]cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asmA)}),
		)
		err := b.validateAuthPlanAssemblyMatch()
		require.NoError(t, err)
	})

	t.Run("Mismatch_DifferentInstances", func(t *testing.T) {
		t.Parallel()
		b := New(
			WithAssembly(asmA),
			WithListener(cell.PrimaryListener, "127.0.0.1:0",
				[]cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asmB)}),
		)
		err := b.validateAuthPlanAssemblyMatch()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "AuthJWTFromAssembly")
		assert.Contains(t, err.Error(), "asm-match-a")
		assert.Contains(t, err.Error(), "asm-match-b")
		assert.Contains(t, err.Error(), cell.PrimaryListener.String())
	})

	t.Run("NilAssembly_NoError", func(t *testing.T) {
		t.Parallel()
		// No WithAssembly — b.assembly is nil; validateAuthPlanAssemblyMatch
		// should return nil immediately.
		b := &Bootstrap{
			listenerConfigs: map[cell.ListenerRef]listenerConfig{
				cell.PrimaryListener: {
					authChain: []cell.ListenerAuth{cell.AuthMTLS{}},
				},
			},
		}
		require.NoError(t, b.validateAuthPlanAssemblyMatch())
	})
}

// ─── TestValidateAuthPlanMTLSBindings ─────────────────────────────────────────

func TestValidateAuthPlanMTLSBindings(t *testing.T) {
	t.Parallel()

	t.Run("ListenerPath_NoTLSConfig", func(t *testing.T) {
		t.Parallel()
		b := bootstrapWithListener(
			cell.InternalListener,
			[]cell.ListenerAuth{cell.AuthMTLS{}},
			nil, // no tls.Config
		)
		err := b.validateAuthPlanMTLSBindings()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "AuthMTLS")
		assert.Contains(t, err.Error(), cell.InternalListener.String())
	})

	t.Run("ListenerPath_LooseClientAuth", func(t *testing.T) {
		t.Parallel()
		b := bootstrapWithListener(
			cell.InternalListener,
			[]cell.ListenerAuth{cell.AuthMTLS{}},
			&tls.Config{
				ClientAuth: tls.NoClientCert, // too loose
				ClientCAs:  x509.NewCertPool(),
			},
		)
		err := b.validateAuthPlanMTLSBindings()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ClientAuth")
	})

	t.Run("ListenerPath_NoClientCAs", func(t *testing.T) {
		t.Parallel()
		b := bootstrapWithListener(
			cell.InternalListener,
			[]cell.ListenerAuth{cell.AuthMTLS{}},
			&tls.Config{
				ClientAuth: tls.RequireAndVerifyClientCert,
				ClientCAs:  nil, // missing
			},
		)
		err := b.validateAuthPlanMTLSBindings()
		require.Error(t, err)
		assert.Contains(t, err.Error(), "ClientCAs")
	})

	t.Run("ListenerPath_AllValid", func(t *testing.T) {
		t.Parallel()
		b := bootstrapWithListener(
			cell.InternalListener,
			[]cell.ListenerAuth{cell.AuthMTLS{}},
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
		chain   []cell.ListenerAuth
		wantErr bool
	}{
		{name: "AuthNone alone accepted", chain: []cell.ListenerAuth{cell.AuthNone{}}},
		{name: "guard alone accepted", chain: []cell.ListenerAuth{cell.AuthMTLS{}}},
		{name: "AuthNone mixed with guard rejected", chain: []cell.ListenerAuth{cell.AuthNone{}, cell.AuthMTLS{}}, wantErr: true},
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
			assert.Contains(t, err.Error(), "AuthNone cannot be mixed")
			assert.Contains(t, err.Error(), cell.PrimaryListener.String())
		})
	}
}

func TestValidateAuthServiceTokenPlans(t *testing.T) {
	t.Parallel()

	validPlan := cell.MustNewAuthServiceToken(&applyStubNonceStore{}, &applyStubHMACKeyring{})

	tests := []struct {
		name    string
		chain   []cell.ListenerAuth
		wantErr string
	}{
		{
			name:  "accepts one service token",
			chain: []cell.ListenerAuth{validPlan},
		},
		{
			name:  "accepts mtls plus one service token",
			chain: []cell.ListenerAuth{cell.AuthMTLS{}, validPlan},
		},
		{
			name: "rejects duplicate service token",
			chain: []cell.ListenerAuth{
				validPlan,
				validPlan,
			},
			wantErr: "at most one AuthServiceToken",
		},
		{
			name: "rejects nil nonce store",
			chain: []cell.ListenerAuth{
				cell.AuthServiceToken{Store: nil, Ring: &applyStubHMACKeyring{}},
			},
			wantErr: "Store must not be nil",
		},
		{
			name: "rejects nil keyring",
			chain: []cell.ListenerAuth{
				cell.AuthServiceToken{Store: &applyStubNonceStore{}, Ring: nil},
			},
			wantErr: "Ring must not be nil",
		},
		{
			name: "rejects noop nonce store literal",
			chain: []cell.ListenerAuth{
				cell.AuthServiceToken{Store: &applyNoopNonceStore{}, Ring: &applyStubHMACKeyring{}},
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
			assert.Contains(t, err.Error(), tc.wantErr)
			assert.Contains(t, err.Error(), cell.InternalListener.String())
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
		chain   []cell.ListenerAuth
		wantErr bool
		errMsg  string
	}{
		{"empty", nil, false, ""},
		{"jwt_alone", []cell.ListenerAuth{cell.MustNewAuthJWT(verifier)}, false, ""},
		{"jwt_from_assembly_alone", []cell.ListenerAuth{cell.MustNewAuthJWTFromAssembly(asm)}, false, ""},
		{
			"jwt_not_first",
			[]cell.ListenerAuth{cell.AuthMTLS{}, cell.MustNewAuthJWT(verifier)},
			true, "sole/first",
		},
		{
			"dual_jwt",
			[]cell.ListenerAuth{cell.MustNewAuthJWT(verifier), cell.MustNewAuthJWT(verifier)},
			true, "at most one",
		},
		{
			"jwt_and_jwt_from_assembly",
			[]cell.ListenerAuth{cell.MustNewAuthJWT(verifier), cell.MustNewAuthJWTFromAssembly(asm)},
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
					assert.Contains(t, err.Error(), tc.errMsg)
				}
			} else {
				require.NoError(t, err)
			}
		})
	}
}
