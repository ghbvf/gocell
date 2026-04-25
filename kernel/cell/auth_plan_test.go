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
		{"AuthJWT", cell.NewAuthJWT(verifier), "jwt"},
		{"AuthJWTFromAssembly", cell.NewAuthJWTFromAssembly(asm), "jwt"},
		{"AuthMTLS", cell.AuthMTLS{}, "mtls"},
		{"AuthServiceToken", cell.NewAuthServiceToken(store, ring), "service-token"},
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
	cell.NewAuthJWTFromAssembly(nil) //nolint:staticcheck // SA1012: deliberate nil arg to test panic guard
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

// shortHMACKeyring intentionally returns a secret below MinHMACKeyBytes to
// exercise the construction-time strength check.
type shortHMACKeyring struct{}

func (*shortHMACKeyring) Current() []byte   { return []byte("31-byte-secret-padding---------") }
func (*shortHMACKeyring) Secrets() [][]byte { return [][]byte{(&shortHMACKeyring{}).Current()} }

func TestNewAuthServiceToken_RejectsShortKey(t *testing.T) {
	t.Parallel()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic for short HMAC ring.Current(), got none")
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panic value is not string: %T %v", r, r)
		}
		if !strings.Contains(msg, "MinHMACKeyBytes") && !strings.Contains(msg, "minimum is 32") {
			t.Errorf("panic message must mention MinHMACKeyBytes / minimum=32: %q", msg)
		}
	}()
	// Pre-condition: stub returns 31 bytes (one short of MinHMACKeyBytes=32).
	if got := len((&shortHMACKeyring{}).Current()); got >= cell.MinHMACKeyBytes {
		t.Fatalf("test fixture broken: shortHMACKeyring.Current() returned %d bytes, want < %d",
			got, cell.MinHMACKeyBytes)
	}
	cell.NewAuthServiceToken(&stubNonceStore{}, &shortHMACKeyring{})
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

func (s *stubNonceStore) Kind() cell.NonceStoreKind { return cell.NonceStoreKindNoop }

// stubHMACKeyring satisfies cell.HMACKeyring.
type stubHMACKeyring struct{}

func (s *stubHMACKeyring) Current() []byte   { return []byte("stub-secret-32-bytes-padding-----") }
func (s *stubHMACKeyring) Secrets() [][]byte { return [][]byte{s.Current()} }
