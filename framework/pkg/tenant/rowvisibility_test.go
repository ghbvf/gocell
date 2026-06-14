package tenant

import "testing"

type newRowVisibilityCase struct {
	name    string
	scope   RowScope
	subject string
	wantErr bool
}

type sqlPredicateCase struct {
	name       string
	scope      RowScope
	subject    string
	ownerCol   string
	wantApply  bool
	wantPrefix string
	wantArg    any
	wantErr    bool
}

func TestNewRowVisibility(t *testing.T) {
	t.Parallel()
	tests := []newRowVisibilityCase{
		{name: "self requires subject", scope: RowScopeSelf, subject: "u1"},
		{name: "self empty subject rejected", scope: RowScopeSelf, subject: "", wantErr: true},
		{name: "device requires subject", scope: RowScopeDevice, subject: "d1"},
		{name: "device empty subject rejected", scope: RowScopeDevice, subject: "", wantErr: true},
		{name: "tenant allows empty subject", scope: RowScopeTenant, subject: ""},
		{name: "tenant non-empty subject rejected", scope: RowScopeTenant, subject: "u1", wantErr: true},
		// RowScopeAll is no longer mintable via NewRowVisibility (#1760): it is
		// sealed behind NewCrossTenantVisibility. BOTH forms are rejected here —
		// the general constructor is provably incapable of minting an All obligation.
		{name: "all empty subject rejected (sealed)", scope: RowScopeAll, subject: "", wantErr: true},
		{name: "all non-empty subject rejected", scope: RowScopeAll, subject: "u1", wantErr: true},
		{name: "zero scope rejected", scope: RowScope(0), subject: "u1", wantErr: true},
		{name: "out-of-range scope rejected", scope: RowScope(9), subject: "u1", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertNewRowVisibility(t, tt)
		})
	}
}

func assertNewRowVisibility(t *testing.T, tt newRowVisibilityCase) {
	t.Helper()

	v, err := NewRowVisibility(tt.scope, tt.subject)
	if tt.wantErr {
		if err == nil {
			t.Fatalf("NewRowVisibility(%v, %q) = nil err, want err", tt.scope, tt.subject)
		}
		return
	}
	if err != nil {
		t.Fatalf("NewRowVisibility(%v, %q) unexpected err: %v", tt.scope, tt.subject, err)
	}
	if v.Scope() != tt.scope {
		t.Errorf("Scope() = %v, want %v", v.Scope(), tt.scope)
	}
	if v.Subject() != tt.subject {
		t.Errorf("Subject() = %q, want %q", v.Subject(), tt.subject)
	}
	if err := v.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil", err)
	}
}

func TestRowVisibility_ZeroValueInvalid(t *testing.T) {
	t.Parallel()
	var zero RowVisibility
	if err := zero.Validate(); err == nil {
		t.Fatal("zero RowVisibility.Validate() = nil, want err (zero scope is invalid)")
	}
}

func TestRowVisibility_SQLPredicate(t *testing.T) {
	t.Parallel()
	tests := []sqlPredicateCase{
		{
			name: "self emits owner predicate", scope: RowScopeSelf, subject: "u1", ownerCol: "actor_id",
			wantApply: true, wantPrefix: " AND actor_id = ", wantArg: "u1",
		},
		{
			name: "device emits owner predicate", scope: RowScopeDevice, subject: "d1", ownerCol: "device_id",
			wantApply: true, wantPrefix: " AND device_id = ", wantArg: "d1",
		},
		{
			name: "tenant emits no owner predicate", scope: RowScopeTenant, subject: "", ownerCol: "actor_id",
			wantApply: false,
		},
		{
			// all's cross-tenant boundary bypass is a tenant-boundary concern (PR-5);
			// the owner dimension is unrestricted, same as tenant.
			name: "all emits no owner predicate", scope: RowScopeAll, subject: "", ownerCol: "actor_id",
			wantApply: false,
		},
		{
			name: "invalid owner column rejected", scope: RowScopeSelf, subject: "u1", ownerCol: "actor_id; DROP TABLE",
			wantErr: true,
		},
		{
			name: "uppercase owner column rejected", scope: RowScopeSelf, subject: "u1", ownerCol: "ActorID",
			wantErr: true,
		},
		{
			name: "empty owner column rejected", scope: RowScopeSelf, subject: "u1", ownerCol: "",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertSQLPredicate(t, tt)
		})
	}
}

// mustRowVis mints a RowVisibility for tests, routing RowScopeAll through the
// sealed NewCrossTenantVisibility funnel (#1760: NewRowVisibility now rejects
// All). All other scopes go through the general constructor so a malformed
// non-All obligation still surfaces its construction error.
func mustRowVis(t *testing.T, scope RowScope, subject string) RowVisibility {
	t.Helper()
	if scope == RowScopeAll {
		return NewCrossTenantVisibility().Visibility()
	}
	v, err := NewRowVisibility(scope, subject)
	if err != nil {
		t.Fatalf("NewRowVisibility(%v, %q): %v", scope, subject, err)
	}
	return v
}

func assertSQLPredicate(t *testing.T, tt sqlPredicateCase) {
	t.Helper()

	v := mustRowVis(t, tt.scope, tt.subject)
	p, err := v.SQLPredicate(tt.ownerCol)
	if tt.wantErr {
		if err == nil {
			t.Fatalf("SQLPredicate(%q) = nil err, want err", tt.ownerCol)
		}
		return
	}
	if err != nil {
		t.Fatalf("SQLPredicate(%q) unexpected err: %v", tt.ownerCol, err)
	}
	if p.Apply != tt.wantApply {
		t.Errorf("Apply = %v, want %v", p.Apply, tt.wantApply)
	}
	if p.Apply {
		if p.Prefix != tt.wantPrefix {
			t.Errorf("Prefix = %q, want %q", p.Prefix, tt.wantPrefix)
		}
		if p.Arg != tt.wantArg {
			t.Errorf("Arg = %v, want %v", p.Arg, tt.wantArg)
		}
	}
}

func TestRowVisibility_SQLPredicate_ZeroValueRejected(t *testing.T) {
	t.Parallel()
	var zero RowVisibility
	if _, err := zero.SQLPredicate("actor_id"); err == nil {
		t.Fatal("zero RowVisibility.SQLPredicate = nil err, want err")
	}
}

func TestRowVisibility_Allows(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		scope      RowScope
		subject    string
		ownerValue string
		want       bool
	}{
		{name: "self matches own row", scope: RowScopeSelf, subject: "u1", ownerValue: "u1", want: true},
		{name: "self rejects other row", scope: RowScopeSelf, subject: "u1", ownerValue: "u2", want: false},
		{name: "device matches own row", scope: RowScopeDevice, subject: "d1", ownerValue: "d1", want: true},
		{name: "device rejects user row", scope: RowScopeDevice, subject: "d1", ownerValue: "u1", want: false},
		{name: "tenant allows any row", scope: RowScopeTenant, subject: "", ownerValue: "anyone", want: true},
		{name: "all allows any row", scope: RowScopeAll, subject: "", ownerValue: "anyone", want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			v := mustRowVis(t, tt.scope, tt.subject)
			if got := v.Allows(tt.ownerValue); got != tt.want {
				t.Errorf("Allows(%q) = %v, want %v", tt.ownerValue, got, tt.want)
			}
		})
	}
}
