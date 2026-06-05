package tenant

import (
	"context"
	"testing"
)

func TestScopeRoundTrip(t *testing.T) {
	t.Parallel()
	const tid TenantID = "11111111-1111-1111-1111-111111111111"
	ctx := WithScope(context.Background(), tid)
	got, ok := ScopeFromContext(ctx)
	if !ok {
		t.Fatalf("ScopeFromContext: ok = false, want true")
	}
	if got != tid {
		t.Fatalf("ScopeFromContext = %q, want %q", got, tid)
	}
}

func TestScopeAbsent(t *testing.T) {
	t.Parallel()
	got, ok := ScopeFromContext(context.Background())
	if ok {
		t.Fatalf("ScopeFromContext on bare ctx: ok = true, want false")
	}
	if got != "" {
		t.Fatalf("ScopeFromContext on bare ctx: value = %q, want empty", got)
	}
}

// TestScopeDistinctFromPrincipalKey asserts the dedicated scope key does not
// collide with any other context value: a child context that only set the scope
// must not satisfy a lookup under a different key type, and vice versa. This is
// the structural guarantee that the scope key is namespaced by its unexported
// type identity.
func TestScopeDistinctFromPrincipalKey(t *testing.T) {
	t.Parallel()
	type otherKey struct{}
	const tid TenantID = "22222222-2222-2222-2222-222222222222"

	ctx := context.WithValue(context.Background(), otherKey{}, "principal-value")
	// scope not set yet → absent even though another key holds a TenantID-shaped value
	if _, ok := ScopeFromContext(ctx); ok {
		t.Fatalf("ScopeFromContext leaked from unrelated key")
	}
	ctx = WithScope(ctx, tid)
	got, ok := ScopeFromContext(ctx)
	if !ok || got != tid {
		t.Fatalf("ScopeFromContext after WithScope = (%q,%v), want (%q,true)", got, ok, tid)
	}
	// the unrelated key is still independently readable
	if v, _ := ctx.Value(otherKey{}).(string); v != "principal-value" {
		t.Fatalf("unrelated key clobbered: %q", v)
	}
}

// TestScopeOverwrite confirms the last WithScope wins (context value shadowing),
// matching how a nested derivation would re-scope.
func TestScopeOverwrite(t *testing.T) {
	t.Parallel()
	ctx := WithScope(context.Background(), "33333333-3333-3333-3333-333333333333")
	ctx = WithScope(ctx, "44444444-4444-4444-4444-444444444444")
	got, _ := ScopeFromContext(ctx)
	if got != "44444444-4444-4444-4444-444444444444" {
		t.Fatalf("ScopeFromContext after re-scope = %q, want the newer value", got)
	}
}
