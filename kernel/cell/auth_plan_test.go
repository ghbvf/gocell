package cell_test

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/kernel/cell"
)

// ─── Compile-time interface assertions ────────────────────────────────────────

// AuthPlan sealed interface assertions.
var _ cell.AuthPlan = cell.AuthNone{}
var _ cell.AuthPlan = cell.AuthJWT{}
var _ cell.AuthPlan = cell.AuthJWTFromAssembly{}
var _ cell.AuthPlan = cell.AuthMTLS{}
var _ cell.AuthPlan = cell.AuthServiceToken{}
var _ cell.AuthPlan = cell.AuthVerboseToken{}

// ListenerAuth assertions: plans that CAN be used as listener-level auth.
var _ cell.ListenerAuth = cell.AuthNone{}
var _ cell.ListenerAuth = cell.AuthJWT{}
var _ cell.ListenerAuth = cell.AuthJWTFromAssembly{}
var _ cell.ListenerAuth = cell.AuthMTLS{}
var _ cell.ListenerAuth = cell.AuthServiceToken{}

// GroupAuth assertions: plans that CAN be used as route-group-level auth.
var _ cell.GroupAuth = cell.AuthNone{}
var _ cell.GroupAuth = cell.AuthMTLS{}
var _ cell.GroupAuth = cell.AuthServiceToken{}
var _ cell.GroupAuth = cell.AuthVerboseToken{}

// Segregation: AuthJWT/AuthJWTFromAssembly MUST NOT implement GroupAuth.
// This is enforced at compile-time; the negative check is verified by the
// archtest fixture in tools/archtest/celltest_segregation/.
//
// Segregation: AuthVerboseToken MUST NOT implement ListenerAuth.
// Same — see archtest fixture.

// ─── Describe() golden values ─────────────────────────────────────────────────

func TestAuthPlan_Describe(t *testing.T) {
	t.Parallel()

	verifier := &stubVerifier{}
	asm := &stubAssemblyRef{id: "test"}
	store := &stubNonceStore{}
	ring := &stubHMACKeyring{}

	tests := []struct {
		name string
		plan cell.AuthPlan
		want string
	}{
		{"AuthNone", cell.AuthNone{}, "none"},
		{"AuthJWT", cell.NewAuthJWT(verifier), "jwt"},
		{"AuthJWTFromAssembly", cell.NewAuthJWTFromAssembly(asm), "jwt-from-assembly"},
		{"AuthMTLS", cell.AuthMTLS{}, "mtls"},
		{"AuthServiceToken", cell.NewAuthServiceToken(store, ring), "service-token"},
		{"AuthVerboseToken", cell.NewAuthVerboseToken("X-Token", "secret"), "verbose-token"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := tc.plan.Describe(); got != tc.want {
				t.Errorf("Describe() = %q, want %q", got, tc.want)
			}
		})
	}
}

// ─── AuthKind discriminant ────────────────────────────────────────────────────

func TestAuthPlan_AuthKind(t *testing.T) {
	t.Parallel()

	// We expose AuthKind indirectly via the plan struct — test that the constants
	// are distinct (no accidental iota collision).
	kinds := []cell.AuthKind{
		cell.AuthKindNone,
		cell.AuthKindJWT,
		cell.AuthKindJWTFromAssembly,
		cell.AuthKindMTLS,
		cell.AuthKindServiceToken,
		cell.AuthKindVerboseToken,
	}
	seen := make(map[cell.AuthKind]struct{})
	for _, k := range kinds {
		if _, dup := seen[k]; dup {
			t.Errorf("duplicate AuthKind value %d", k)
		}
		seen[k] = struct{}{}
	}
}

// ─── Constructor nil/empty panic guards ───────────────────────────────────────

func TestNewAuthJWT_NilPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil verifier, got none")
		}
	}()
	cell.NewAuthJWT(nil)
}

func TestNewAuthJWTFromAssembly_NilPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil assembly, got none")
		}
	}()
	cell.NewAuthJWTFromAssembly(nil) //nolint:staticcheck
}

func TestNewAuthServiceToken_NilStorePanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil store, got none")
		}
	}()
	cell.NewAuthServiceToken(nil, &stubHMACKeyring{})
}

func TestNewAuthServiceToken_NilRingPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for nil ring, got none")
		}
	}()
	cell.NewAuthServiceToken(&stubNonceStore{}, nil)
}

func TestNewAuthVerboseToken_EmptyHeaderPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for empty header, got none")
		}
	}()
	cell.NewAuthVerboseToken("", "token")
}

func TestNewAuthVerboseToken_EmptyTokenPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic for empty token, got none")
		}
	}()
	cell.NewAuthVerboseToken("X-Token", "")
}

// ─── AuthJWT fields ───────────────────────────────────────────────────────────

func TestNewAuthJWT_StoresVerifier(t *testing.T) {
	t.Parallel()
	v := &stubVerifier{}
	p := cell.NewAuthJWT(v)
	if p.Verifier != v {
		t.Errorf("NewAuthJWT Verifier field mismatch: got %v, want %v", p.Verifier, v)
	}
}

// ─── AuthJWTFromAssembly atomic pointer ───────────────────────────────────────

func TestAuthJWTFromAssembly_ResolvedVerifier(t *testing.T) {
	t.Parallel()

	asm := &stubAssemblyRef{id: "test"}
	p := cell.NewAuthJWTFromAssembly(asm)

	// Before SetResolved, ResolvedVerifier returns nil.
	if got := p.ResolvedVerifier(); got != nil {
		t.Errorf("before SetResolved: expected nil, got %v", got)
	}

	v := &stubVerifier{}
	p.SetResolved(v)

	if got := p.ResolvedVerifier(); got != v {
		t.Errorf("after SetResolved: got %v, want %v", got, v)
	}
}

// ─── AuthVerboseToken hashed token ────────────────────────────────────────────

func TestNewAuthVerboseToken_DifferentHashForDifferentTokens(t *testing.T) {
	t.Parallel()
	a := cell.NewAuthVerboseToken("X-Token", "secret-a")
	b := cell.NewAuthVerboseToken("X-Token", "secret-b")
	if a.HashedToken == b.HashedToken {
		t.Error("different tokens should produce different hashes")
	}
}

func TestNewAuthVerboseToken_SameHashForSameToken(t *testing.T) {
	t.Parallel()
	a := cell.NewAuthVerboseToken("X-Token", "secret")
	b := cell.NewAuthVerboseToken("X-Token", "secret")
	if a.HashedToken != b.HashedToken {
		t.Error("same token should produce same hash")
	}
}

// ─── TokenIntent ──────────────────────────────────────────────────────────────

func TestTokenIntent_IsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		intent cell.TokenIntent
		valid  bool
	}{
		{cell.TokenIntentAccess, true},
		{cell.TokenIntent("refresh"), false},
		{cell.TokenIntent(""), false},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(string(tc.intent), func(t *testing.T) {
			t.Parallel()
			if got := tc.intent.IsValid(); got != tc.valid {
				t.Errorf("IsValid() = %v, want %v", got, tc.valid)
			}
		})
	}
}

// ─── Test stubs ───────────────────────────────────────────────────────────────

// stubAssemblyRef satisfies cell.AssemblyRef.
type stubAssemblyRef struct{ id string }

func (s *stubAssemblyRef) ID() string       { return s.id }
func (s *stubAssemblyRef) CellIDs() []string { return nil }

// stubVerifier satisfies cell.IntentTokenVerifier.
type stubVerifier struct{}

func (s *stubVerifier) VerifyIntent(_ context.Context, _ string, _ cell.TokenIntent) (cell.Claims, error) {
	return cell.Claims{}, nil
}

// stubNonceStore satisfies cell.NonceStore.
type stubNonceStore struct{}

func (s *stubNonceStore) CheckAndMark(_ context.Context, _ string) error {
	return nil
}

func (s *stubNonceStore) Kind() cell.NonceStoreKind { return cell.NonceStoreKindNoop }

// stubHMACKeyring satisfies cell.HMACKeyring.
type stubHMACKeyring struct{}

func (s *stubHMACKeyring) Current() []byte    { return []byte("stub-secret-32-bytes-padding-----") }
func (s *stubHMACKeyring) Secrets() [][]byte  { return [][]byte{s.Current()} }
