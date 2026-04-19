package auth

import (
	"context"
	"testing"
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

func TestMustFromContext_OK(t *testing.T) {
	p := &Principal{Kind: PrincipalService, Subject: "svc"}
	ctx := WithPrincipal(context.Background(), p)
	got := MustFromContext(ctx)
	if got != p {
		t.Error("expected same pointer")
	}
}

func TestMustFromContext_Panics(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic")
		}
		msg, ok := r.(string)
		if !ok || msg != "auth: principal not in context" {
			t.Errorf("unexpected panic value: %v", r)
		}
	}()
	MustFromContext(context.Background())
}

func TestDefaultServiceRoles(t *testing.T) {
	tests := []struct {
		name    string
		service string
		want    []string
	}{
		{"known service", "gocell-internal", []string{"role:internal-admin"}},
		{"unknown service", "some-other-service", nil},
		{"empty string", "", nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := DefaultServiceRoles(tc.service)
			if len(got) != len(tc.want) {
				t.Errorf("DefaultServiceRoles(%q) = %v, want %v", tc.service, got, tc.want)
				return
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("DefaultServiceRoles(%q)[%d] = %q, want %q", tc.service, i, got[i], tc.want[i])
				}
			}
		})
	}
}
