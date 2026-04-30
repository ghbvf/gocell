package cell_test

import (
	"context"
	"strings"
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

// ListenerAuth assertions: every AuthPlan in the closed enumeration must
// satisfy ListenerAuth. Auth scheme is a listener-scope concern.
var _ cell.ListenerAuth = cell.AuthNone{}
var _ cell.ListenerAuth = cell.AuthJWT{}
var _ cell.ListenerAuth = cell.AuthJWTFromAssembly{}
var _ cell.ListenerAuth = cell.AuthMTLS{}
var _ cell.ListenerAuth = cell.AuthServiceToken{}

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
		{"AuthJWT", cell.MustNewAuthJWT(verifier), "jwt"},
		{"AuthJWTFromAssembly", cell.MustNewAuthJWTFromAssembly(asm), "jwt"},
		{"AuthMTLS", cell.AuthMTLS{}, "mtls"},
		{"AuthServiceToken", cell.MustNewAuthServiceToken(store, ring), "service-token"},
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
	kinds := []cell.AuthKind{
		cell.AuthKindNone,
		cell.AuthKindJWT,
		cell.AuthKindJWTFromAssembly,
		cell.AuthKindMTLS,
		cell.AuthKindServiceToken,
	}
	seen := make(map[cell.AuthKind]struct{})
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
	if _, err := cell.NewAuthJWT(nil); err == nil {
		t.Error("expected error for nil verifier, got nil")
	}
}

func TestNewAuthJWT_TypedNilReturnsError(t *testing.T) {
	t.Parallel()
	var verifier *stubVerifier
	if _, err := cell.NewAuthJWT(verifier); err == nil {
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
	cell.MustNewAuthJWT(nil)
}

func TestNewAuthJWTFromAssembly_NilReturnsError(t *testing.T) {
	t.Parallel()
	if _, err := cell.NewAuthJWTFromAssembly(nil); err == nil { //nolint:staticcheck // SA1012: deliberate nil arg
		t.Error("expected error for nil assembly, got nil")
	}
}

func TestNewAuthJWTFromAssembly_TypedNilReturnsError(t *testing.T) {
	t.Parallel()
	var asm *stubAssemblyRef
	if _, err := cell.NewAuthJWTFromAssembly(asm); err == nil {
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
	cell.MustNewAuthJWTFromAssembly(nil) //nolint:staticcheck // SA1012: deliberate nil arg to test panic guard
}

func TestNewAuthServiceToken_NilStoreReturnsError(t *testing.T) {
	t.Parallel()
	if _, err := cell.NewAuthServiceToken(nil, &stubHMACKeyring{}); err == nil {
		t.Error("expected error for nil store, got nil")
	}
}

func TestNewAuthServiceToken_TypedNilStoreReturnsError(t *testing.T) {
	t.Parallel()
	var store *stubNonceStore
	if _, err := cell.NewAuthServiceToken(store, &stubHMACKeyring{}); err == nil {
		t.Error("expected error for typed-nil store, got nil")
	}
}

func TestNewAuthServiceToken_NilRingReturnsError(t *testing.T) {
	t.Parallel()
	if _, err := cell.NewAuthServiceToken(&stubNonceStore{}, nil); err == nil {
		t.Error("expected error for nil ring, got nil")
	}
}

func TestNewAuthServiceToken_TypedNilRingReturnsError(t *testing.T) {
	t.Parallel()
	var ring *stubHMACKeyring
	if _, err := cell.NewAuthServiceToken(&stubNonceStore{}, ring); err == nil {
		t.Error("expected error for typed-nil ring, got nil")
	}
}

func TestNewAuthServiceToken_RejectsNoopNonceStore(t *testing.T) {
	t.Parallel()

	_, err := cell.NewAuthServiceToken(&stubNoopNonceStore{}, &stubHMACKeyring{})

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
	cell.MustNewAuthServiceToken(nil, &stubHMACKeyring{})
}

// shortHMACKeyring intentionally returns a secret below MinHMACKeyBytes to
// exercise the construction-time strength check.
type shortHMACKeyring struct{}

func (*shortHMACKeyring) Current() []byte   { return []byte("31-byte-secret-padding---------") }
func (*shortHMACKeyring) Secrets() [][]byte { return [][]byte{(&shortHMACKeyring{}).Current()} }

func TestNewAuthServiceToken_RejectsShortKey(t *testing.T) {
	t.Parallel()
	// Pre-condition: stub returns 31 bytes (one short of MinHMACKeyBytes=32).
	if got := len((&shortHMACKeyring{}).Current()); got >= cell.MinHMACKeyBytes {
		t.Fatalf("test fixture broken: shortHMACKeyring.Current() returned %d bytes, want < %d",
			got, cell.MinHMACKeyBytes)
	}
	_, err := cell.NewAuthServiceToken(&stubNonceStore{}, &shortHMACKeyring{})
	if err == nil {
		t.Fatal("expected error for short HMAC ring.Current(), got nil")
	}
	// Error message format (auth_plan.go::NewAuthServiceToken):
	//   "cell: NewAuthServiceToken HMAC ring.Current() returned 31 bytes, minimum is 32 (NIST SP 800-107)"
	if !strings.Contains(err.Error(), "minimum is 32") {
		t.Errorf("error message must mention 'minimum is 32': %q", err.Error())
	}
}

// ─── AuthJWT fields ───────────────────────────────────────────────────────────

func TestNewAuthJWT_StoresVerifier(t *testing.T) {
	t.Parallel()
	v := &stubVerifier{}
	p, err := cell.NewAuthJWT(v)
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
	p, err := cell.NewAuthJWTFromAssembly(asm)
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

func (s *stubAssemblyRef) ID() string        { return s.id }
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

func (s *stubNonceStore) Kind() cell.NonceStoreKind { return cell.NonceStoreKindInMemory }

type stubNoopNonceStore struct{}

func (s *stubNoopNonceStore) CheckAndMark(_ context.Context, _ string) error {
	return nil
}

func (s *stubNoopNonceStore) Kind() cell.NonceStoreKind { return cell.NonceStoreKindNoop }

// stubHMACKeyring satisfies cell.HMACKeyring.
type stubHMACKeyring struct{}

func (s *stubHMACKeyring) Current() []byte   { return []byte("stub-secret-32-bytes-padding-----") }
func (s *stubHMACKeyring) Secrets() [][]byte { return [][]byte{s.Current()} }
