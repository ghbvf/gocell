package auth_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/framework/kernel/auth"
	"github.com/ghbvf/gocell/framework/kernel/auth/authtest"
	"github.com/ghbvf/gocell/framework/kernel/cell"
)

// ─── Compile-time interface assertions ────────────────────────────────────────

// AuthPlan sealed interface assertions.
var (
	_ auth.AuthPlan = auth.AuthNone{}
	_ auth.AuthPlan = auth.AuthJWT{}
	_ auth.AuthPlan = auth.AuthJWTFromAssembly{}
	_ auth.AuthPlan = auth.AuthMTLS{}
	_ auth.AuthPlan = auth.AuthServiceToken{}
)

// ListenerAuth assertions: every AuthPlan in the closed enumeration must
// satisfy ListenerAuth. Auth scheme is a listener-scope concern.
var (
	_ auth.ListenerAuth = auth.AuthNone{}
	_ auth.ListenerAuth = auth.AuthJWT{}
	_ auth.ListenerAuth = auth.AuthJWTFromAssembly{}
	_ auth.ListenerAuth = auth.AuthMTLS{}
	_ auth.ListenerAuth = auth.AuthServiceToken{}
)

// ─── Describe() golden values ─────────────────────────────────────────────────

func TestAuthPlan_Describe(t *testing.T) {
	t.Parallel()

	verifier := &stubVerifier{}
	asm := &stubAssemblyRef{id: "test"}
	store := &stubNonceStore{}
	ring := &stubHMACKeyring{}

	tests := []struct {
		name string
		plan auth.AuthPlan
		want string
	}{
		{"AuthNone", auth.AuthNone{}, "none"},
		{"AuthJWT", authtest.MustAuthJWT(verifier), "jwt"},
		{"AuthJWTFromAssembly", authtest.MustAuthJWTFromAssembly(asm), "jwt"},
		{"AuthMTLS", auth.AuthMTLS{}, "mtls"},
		{"AuthServiceToken", authtest.MustAuthServiceToken(store, ring), "service-token"},
	}

	for _, tc := range tests {
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
	kinds := []auth.AuthKind{
		auth.AuthKindNone,
		auth.AuthKindJWT,
		auth.AuthKindJWTFromAssembly,
		auth.AuthKindMTLS,
		auth.AuthKindServiceToken,
	}
	seen := make(map[auth.AuthKind]struct{})
	for _, k := range kinds {
		if _, dup := seen[k]; dup {
			t.Errorf("duplicate AuthKind value %d", k)
		}
		seen[k] = struct{}{}
	}
}

// ─── Constructor nil/empty error guards ───────────────────────────────────────

func TestNewAuthJWT_NilReturnsError(t *testing.T) {
	t.Parallel()
	if _, err := auth.NewAuthJWT(nil); err == nil {
		t.Error("expected error for nil verifier, got nil")
	}
}

func TestNewAuthJWT_TypedNilReturnsError(t *testing.T) {
	t.Parallel()
	var verifier *stubVerifier
	if _, err := auth.NewAuthJWT(verifier); err == nil {
		t.Error("expected error for typed-nil verifier, got nil")
	}
}

func TestMustNewAuthJWT_NilPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("expected panic for nil verifier, got none")
		}
	}()
	authtest.MustAuthJWT(nil)
}

func TestNewAuthJWTFromAssembly_NilReturnsError(t *testing.T) {
	t.Parallel()
	if _, err := auth.NewAuthJWTFromAssembly(nil); err == nil {
		t.Error("expected error for nil assembly, got nil")
	}
}

func TestNewAuthJWTFromAssembly_TypedNilReturnsError(t *testing.T) {
	t.Parallel()
	var asm *stubAssemblyRef
	if _, err := auth.NewAuthJWTFromAssembly(asm); err == nil {
		t.Error("expected error for typed-nil assembly, got nil")
	}
}

func TestMustNewAuthJWTFromAssembly_NilPanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("expected panic for nil assembly, got none")
		}
	}()
	authtest.MustAuthJWTFromAssembly(nil)
}

func TestNewAuthServiceToken_NilStoreReturnsError(t *testing.T) {
	t.Parallel()
	if _, err := auth.NewAuthServiceToken(nil, &stubHMACKeyring{}); err == nil {
		t.Error("expected error for nil store, got nil")
	}
}

func TestNewAuthServiceToken_TypedNilStoreReturnsError(t *testing.T) {
	t.Parallel()
	var store *stubNonceStore
	if _, err := auth.NewAuthServiceToken(store, &stubHMACKeyring{}); err == nil {
		t.Error("expected error for typed-nil store, got nil")
	}
}

func TestNewAuthServiceToken_NilRingReturnsError(t *testing.T) {
	t.Parallel()
	if _, err := auth.NewAuthServiceToken(&stubNonceStore{}, nil); err == nil {
		t.Error("expected error for nil ring, got nil")
	}
}

func TestNewAuthServiceToken_TypedNilRingReturnsError(t *testing.T) {
	t.Parallel()
	var ring *stubHMACKeyring
	if _, err := auth.NewAuthServiceToken(&stubNonceStore{}, ring); err == nil {
		t.Error("expected error for typed-nil ring, got nil")
	}
}

func TestNewAuthServiceToken_RejectsNoopNonceStore(t *testing.T) {
	t.Parallel()

	_, err := auth.NewAuthServiceToken(&stubNoopNonceStore{}, &stubHMACKeyring{})

	if err == nil {
		t.Fatal("expected error for noop nonce store, got nil")
	}
	if !strings.Contains(err.Error(), "NonceStoreKindNoop") {
		t.Errorf("error message must mention NonceStoreKindNoop: %q", err.Error())
	}
}

func TestMustNewAuthServiceToken_NilStorePanics(t *testing.T) {
	t.Parallel()
	defer func() {
		if recover() == nil {
			t.Error("expected panic for nil store, got none")
		}
	}()
	authtest.MustAuthServiceToken(nil, &stubHMACKeyring{})
}

// shortHMACKeyring intentionally fails Validate (sub-MinHMACKeyBytes) to
// exercise the construction-time strength check.
type shortHMACKeyring struct{}

func (*shortHMACKeyring) SigningSecrets(string) ([][]byte, error) {
	return [][]byte{[]byte("31-byte-secret-padding---------")}, nil
}

func (*shortHMACKeyring) VerifySecrets(string) ([][]byte, error) {
	return [][]byte{[]byte("31-byte-secret-padding---------")}, nil
}

func (*shortHMACKeyring) Validate() error {
	return fmt.Errorf("HMAC subkey too short: minimum is %d bytes", auth.MinHMACKeyBytes)
}

func TestNewAuthServiceToken_RejectsShortKey(t *testing.T) {
	t.Parallel()
	_, err := auth.NewAuthServiceToken(&stubNonceStore{}, &shortHMACKeyring{})
	if err == nil {
		t.Fatal("expected error for short HMAC ring, got nil")
	}
	// NewAuthServiceToken wraps ring.Validate(): "...ring is invalid: HMAC subkey
	// too short: minimum is 32 bytes".
	if !strings.Contains(err.Error(), "minimum is 32") {
		t.Errorf("error message must mention 'minimum is 32': %q", err.Error())
	}
}

// ─── AuthJWT fields ───────────────────────────────────────────────────────────

func TestNewAuthJWT_StoresVerifier(t *testing.T) {
	t.Parallel()
	v := &stubVerifier{}
	p, err := auth.NewAuthJWT(v)
	if err != nil {
		t.Fatalf("NewAuthJWT returned unexpected error: %v", err)
	}
	if p.Verifier != v {
		t.Errorf("NewAuthJWT Verifier field mismatch: got %v, want %v", p.Verifier, v)
	}
}

// ─── AuthJWTFromAssembly atomic pointer ───────────────────────────────────────

func TestAuthJWTFromAssembly_ResolvedVerifier(t *testing.T) {
	t.Parallel()

	asm := &stubAssemblyRef{id: "test"}
	p, err := auth.NewAuthJWTFromAssembly(asm)
	if err != nil {
		t.Fatalf("NewAuthJWTFromAssembly returned unexpected error: %v", err)
	}

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

func TestAuthJWTFromAssembly_IsConstructed(t *testing.T) {
	t.Parallel()

	asm := &stubAssemblyRef{id: "constructed"}
	p, err := auth.NewAuthJWTFromAssembly(asm)
	if err != nil {
		t.Fatalf("NewAuthJWTFromAssembly returned unexpected error: %v", err)
	}
	if !p.IsConstructed() {
		t.Fatal("constructor-built AuthJWTFromAssembly should report constructed")
	}

	if (auth.AuthJWTFromAssembly{}).IsConstructed() {
		t.Fatal("struct-literal AuthJWTFromAssembly should report not constructed")
	}
	if (auth.AuthJWTFromAssembly{Assembly: asm}).IsConstructed() {
		t.Fatal("literal with Assembly but no resolver should report not constructed")
	}
}

// ─── TokenIntent ──────────────────────────────────────────────────────────────

func TestTokenIntent_IsValid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		intent auth.TokenIntent
		valid  bool
	}{
		{auth.TokenIntentAccess, true},
		{auth.TokenIntent("refresh"), false},
		{auth.TokenIntent(""), false},
	}
	for _, tc := range tests {
		t.Run(string(tc.intent), func(t *testing.T) {
			t.Parallel()
			if got := tc.intent.IsValid(); got != tc.valid {
				t.Errorf("IsValid() = %v, want %v", got, tc.valid)
			}
		})
	}
}

// ─── Test stubs ───────────────────────────────────────────────────────────────

// stubAssemblyRef satisfies auth.AssemblyRef. Cell returns nil because the
// kernel/auth tests validate construction-time invariants only; cell lookup
// is exercised end-to-end by runtime/bootstrap tests.
type stubAssemblyRef struct{ id string }

func (s *stubAssemblyRef) ID() string              { return s.id }
func (s *stubAssemblyRef) CellIDs() []string       { return nil }
func (s *stubAssemblyRef) Cell(_ string) cell.Cell { return nil }

// stubVerifier satisfies auth.IntentTokenVerifier.
type stubVerifier struct{}

func (s *stubVerifier) VerifyIntent(_ context.Context, _ string, _ auth.TokenIntent) (auth.Claims, error) {
	return auth.Claims{}, nil
}

// stubNonceStore satisfies auth.NonceStore.
type stubNonceStore struct{}

func (s *stubNonceStore) CheckAndMark(_ context.Context, _ string) error {
	return nil
}

func (s *stubNonceStore) Kind() auth.NonceStoreKind { return auth.NonceStoreKindInMemory }

type stubNoopNonceStore struct{}

func (s *stubNoopNonceStore) CheckAndMark(_ context.Context, _ string) error {
	return nil
}

func (s *stubNoopNonceStore) Kind() auth.NonceStoreKind { return auth.NonceStoreKindNoop }

// stubHMACKeyring satisfies auth.ServiceKeyring.
type stubHMACKeyring struct{}

func (s *stubHMACKeyring) SigningSecrets(string) ([][]byte, error) {
	return [][]byte{[]byte("stub-secret-32-bytes-padding-----")}, nil
}

func (s *stubHMACKeyring) VerifySecrets(string) ([][]byte, error) {
	return [][]byte{[]byte("stub-secret-32-bytes-padding-----")}, nil
}
func (s *stubHMACKeyring) Validate() error { return nil }
