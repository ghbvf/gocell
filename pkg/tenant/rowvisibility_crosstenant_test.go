package tenant

import (
	"errors"
	"testing"

	"github.com/ghbvf/gocell/pkg/errcode"
)

// TestNewRowVisibility_RowScopeAllSealed pins the #1760 general-path closure:
// NewRowVisibility can never mint a RowScopeAll obligation, regardless of
// subject. The rejection is a server-side invariant break (the caller bypassed
// the sealed funnel) → KindInternal / ErrInternal, mirroring
// RowScopeAllUnsupportedError's 5xx collapse.
func TestNewRowVisibility_RowScopeAllSealed(t *testing.T) {
	t.Parallel()
	for _, subject := range []string{"", "u1"} {
		v, err := NewRowVisibility(RowScopeAll, subject)
		if err == nil {
			t.Fatalf("NewRowVisibility(RowScopeAll, %q) = nil err, want sealed rejection", subject)
		}
		if v != (RowVisibility{}) {
			t.Errorf("NewRowVisibility(RowScopeAll, %q) returned non-zero RowVisibility %+v", subject, v)
		}
		var ee *errcode.Error
		if !errors.As(err, &ee) {
			t.Fatalf("NewRowVisibility(RowScopeAll, %q) err is not *errcode.Error: %v", subject, err)
		}
		if ee.Kind != errcode.KindInternal {
			t.Errorf("Kind = %v, want KindInternal", ee.Kind)
		}
		if ee.Code != errcode.ErrInternal {
			t.Errorf("Code = %v, want ErrInternal", ee.Code)
		}
	}
}

// TestNewCrossTenantVisibility verifies the sealed cross-tenant minter produces
// a canonical RowScopeAll obligation whose owner-dimension translation is
// identical to a tenant obligation (no owner predicate; allows every owner).
// The tenant boundary itself is enforced elsewhere (PG RLS / admin read pool).
func TestNewCrossTenantVisibility(t *testing.T) {
	t.Parallel()
	vis := NewCrossTenantVisibility().Visibility()

	if vis.Scope() != RowScopeAll {
		t.Errorf("Scope() = %v, want RowScopeAll", vis.Scope())
	}
	if vis.Subject() != "" {
		t.Errorf("Subject() = %q, want empty", vis.Subject())
	}
	if err := vis.Validate(); err != nil {
		t.Errorf("Validate() = %v, want nil (sealed All value must stay canonical-valid)", err)
	}

	p, err := vis.SQLPredicate("actor_id")
	if err != nil {
		t.Fatalf("SQLPredicate: %v", err)
	}
	if p.Apply {
		t.Errorf("SQLPredicate Apply = true, want false (All has no owner predicate)")
	}

	if !vis.Allows("anyone") {
		t.Errorf("Allows(\"anyone\") = false, want true (All allows every owner)")
	}
}

// TestCrossTenantVisibility_ZeroValueNotAll guards against a forged zero-value
// wrapper laundering an All obligation: the unexported field means a
// CrossTenantVisibility{} literal carries the zero (invalid) RowVisibility, not
// a usable All obligation.
func TestCrossTenantVisibility_ZeroValueNotAll(t *testing.T) {
	t.Parallel()
	var zero CrossTenantVisibility
	if zero.Visibility().Scope() == RowScopeAll {
		t.Fatal("zero CrossTenantVisibility yields a RowScopeAll obligation — wrapper is not sealed")
	}
	if err := zero.Visibility().Validate(); err == nil {
		t.Fatal("zero CrossTenantVisibility.Visibility().Validate() = nil, want err (zero scope invalid)")
	}
}

// TestCrossTenantVisibility_Validate pins the single-source PEP predicate (F2):
// the sealed minter's value passes, but the constructable zero value fails-closed.
// Every cross-tenant read PEP (service + each store impl) routes through this, so
// a zero/invalid obligation can never produce a cross-tenant read.
func TestCrossTenantVisibility_Validate(t *testing.T) {
	t.Parallel()

	if err := NewCrossTenantVisibility().Validate(); err != nil {
		t.Errorf("NewCrossTenantVisibility().Validate() = %v, want nil (canonical All grant)", err)
	}

	var zero CrossTenantVisibility
	if err := zero.Validate(); err == nil {
		t.Fatal("zero CrossTenantVisibility.Validate() = nil, want err (zero/invalid obligation)")
	}
}
