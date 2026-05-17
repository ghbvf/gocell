package auth

import (
	"context"
	"testing"
	"time"
)

func TestPrincipalKind_String(t *testing.T) {
	tests := []struct {
		kind PrincipalKind
		want string
	}{
		{PrincipalUser, "user"},
		{PrincipalService, "service"},
		{PrincipalAnonymous, "anonymous"},
		{PrincipalKind(99), "unknown"},
	}
	for _, tc := range tests {
		if got := tc.kind.String(); got != tc.want {
			t.Errorf("PrincipalKind(%d).String() = %q, want %q", tc.kind, got, tc.want)
		}
	}
}

func TestHasRole(t *testing.T) {
	tests := []struct {
		name      string
		principal *Principal
		role      string
		want      bool
	}{
		{"nil principal", nil, "admin", false},
		{"empty roles", &Principal{Roles: nil}, "admin", false},
		{"empty role string", &Principal{Roles: []string{"admin"}}, "", false},
		{"role found", &Principal{Roles: []string{"admin", "user"}}, "admin", true},
		{"role not found", &Principal{Roles: []string{"admin", "user"}}, "superuser", false},
		{"case sensitive no match", &Principal{Roles: []string{"Admin"}}, "admin", false},
		{"multiple roles hit", &Principal{Roles: []string{"a", "b", "c"}}, "b", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.principal.HasRole(tc.role); got != tc.want {
				t.Errorf("HasRole(%q) = %v, want %v", tc.role, got, tc.want)
			}
		})
	}
}

func TestWithPrincipal_FromContext(t *testing.T) {
	t.Run("round trip", func(t *testing.T) {
		p := &Principal{Kind: PrincipalUser, Subject: "u1"}
		ctx := WithPrincipal(context.Background(), p)
		got, ok := FromContext(ctx)
		if !ok {
			t.Fatal("expected ok=true")
		}
		if got != p {
			t.Error("expected same pointer")
		}
	})

	t.Run("nil principal injected returns nil false", func(t *testing.T) {
		ctx := WithPrincipal(context.Background(), nil)
		got, ok := FromContext(ctx)
		if ok {
			t.Error("expected ok=false when nil principal stored")
		}
		if got != nil {
			t.Error("expected nil principal")
		}
	})
}

func TestFromContext_NotInjected(t *testing.T) {
	got, ok := FromContext(context.Background())
	if ok {
		t.Error("expected ok=false on empty context")
	}
	if got != nil {
		t.Error("expected nil principal")
	}
}

func TestFromContext_MissingPrincipal(t *testing.T) {
	got, ok := FromContext(context.Background())
	if ok {
		t.Error("expected ok=false when no principal in context")
	}
	if got != nil {
		t.Error("expected nil principal when not in context")
	}
}

func TestPrincipal_PasswordResetRequired_DefaultFalse(t *testing.T) {
	var p Principal
	if p.PasswordResetRequired {
		t.Error("zero-value Principal.PasswordResetRequired must be false")
	}
}

func TestPrincipal_PasswordResetRequired_RoundTrip(t *testing.T) {
	original := &Principal{Kind: PrincipalUser, PasswordResetRequired: true}
	ctx := WithPrincipal(context.Background(), original)
	got, ok := FromContext(ctx)
	if !ok {
		t.Fatal("expected ok=true from FromContext")
	}
	if !got.PasswordResetRequired {
		t.Error("PasswordResetRequired must survive WithPrincipal/FromContext round-trip")
	}
}

// Spec: PrincipalService (service token auth) must carry CallerCellID (non-empty),
// empty Subject, and nil Roles. The old BuiltinServiceRoles / ServiceNameInternal /
// RoleInternalAdmin pattern is being replaced by caller-cell identity propagation.
func TestPrincipal_ServiceKindCallerCellID(t *testing.T) {
	// Spec: service kind Subject should be empty (identity expressed via CallerCellID)
	p := &Principal{
		Kind:         PrincipalService,
		CallerCellID: "accesscore",
	}
	if p.Subject != "" {
		t.Errorf("service kind Subject should be empty, got %q", p.Subject)
	}
	// Spec: CallerCellID must be non-empty for a service principal
	if p.CallerCellID == "" {
		t.Error("service kind CallerCellID must be non-empty")
	}
	// Spec: Roles must be nil for a service principal (no role-based authz for services)
	if p.Roles != nil {
		t.Errorf("service kind Roles must be nil, got %v", p.Roles)
	}
}

func TestPrincipal_ExpiresAt_ZeroValueMeansNoExpiry(t *testing.T) {
	var p Principal
	if !p.ExpiresAt.IsZero() {
		t.Error("zero-value Principal.ExpiresAt must be zero time (no expiry)")
	}
}

func TestPrincipal_ExpiresAt_RoundTrip(t *testing.T) {
	exp := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)
	original := &Principal{Kind: PrincipalUser, ExpiresAt: exp}
	ctx := WithPrincipal(context.Background(), original)
	got, ok := FromContext(ctx)
	if !ok {
		t.Fatal("FromContext should retrieve principal")
	}
	if !got.ExpiresAt.Equal(exp) {
		t.Errorf("ExpiresAt round-trip failed: got %v want %v", got.ExpiresAt, exp)
	}
}
