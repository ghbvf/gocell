package postgres

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
	"github.com/ghbvf/gocell/pkg/tenant"
)

const guardTestTenant tenant.TenantID = "11111111-1111-1111-1111-111111111111"

func TestTenantScopePrepareConn(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ctx     context.Context
		wantErr bool
	}{
		{
			name:    "tenant-less ctx (probe/migration) is allowed",
			ctx:     context.Background(),
			wantErr: false,
		},
		{
			name:    "principal-only ctx (no scope) is allowed — non-RLS reads must not false-positive",
			ctx:     ctxkeys.WithTenantID(context.Background(), string(guardTestTenant)),
			wantErr: false,
		},
		{
			name:    "scoped ctx WITHOUT RunInTx marker is rejected (the caught bug)",
			ctx:     tenant.WithScope(context.Background(), guardTestTenant),
			wantErr: true,
		},
		{
			name:    "scoped ctx WITH RunInTx marker is allowed",
			ctx:     withTxAcquireIntent(tenant.WithScope(context.Background(), guardTestTenant)),
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// PrepareConn keep-flag is always true (we never destroy the conn);
			// only the error channel differs.
			keep, err := tenantScopePrepareConn(tt.ctx, nil)
			if !keep {
				t.Fatalf("tenantScopePrepareConn keep = false, want true (conn must stay healthy)")
			}
			if (err != nil) != tt.wantErr {
				t.Fatalf("tenantScopePrepareConn err = %v, wantErr = %v", err, tt.wantErr)
			}
		})
	}
}

func TestTenantScopeForTx(t *testing.T) {
	t.Parallel()
	const principal tenant.TenantID = "22222222-2222-2222-2222-222222222222"

	t.Run("absent → not present", func(t *testing.T) {
		t.Parallel()
		if _, ok := tenantScopeForTx(context.Background()); ok {
			t.Fatalf("tenantScopeForTx on bare ctx: ok = true, want false")
		}
	})

	t.Run("ctxkeys principal fallback", func(t *testing.T) {
		t.Parallel()
		ctx := ctxkeys.WithTenantID(context.Background(), string(principal))
		got, ok := tenantScopeForTx(ctx)
		if !ok || got != principal {
			t.Fatalf("tenantScopeForTx = (%q,%v), want (%q,true)", got, ok, principal)
		}
	})

	t.Run("explicit scope wins over principal", func(t *testing.T) {
		t.Parallel()
		ctx := ctxkeys.WithTenantID(context.Background(), string(principal))
		ctx = tenant.WithScope(ctx, guardTestTenant)
		got, ok := tenantScopeForTx(ctx)
		if !ok || got != guardTestTenant {
			t.Fatalf("tenantScopeForTx = (%q,%v), want explicit scope (%q,true)", got, ok, guardTestTenant)
		}
	})

	t.Run("empty ctxkeys value treated as absent", func(t *testing.T) {
		t.Parallel()
		ctx := ctxkeys.WithTenantID(context.Background(), "")
		if _, ok := tenantScopeForTx(ctx); ok {
			t.Fatalf("tenantScopeForTx with empty ctxkeys: ok = true, want false")
		}
	})
}

func TestSetLocalTenantFailsClosedOnInvalidScope(t *testing.T) {
	t.Parallel()
	// A present-but-non-canonical scope must fail BEFORE any tx.Exec (Validate
	// rejects it), so a nil tx is safe here — reaching tx.Exec would panic and
	// fail the test, proving the fail-closed guard runs first.
	ctx := tenant.WithScope(context.Background(), tenant.TenantID("not-a-uuid"))
	if err := setLocalTenant(ctx, nil); err == nil {
		t.Fatalf("setLocalTenant with invalid scope: err = nil, want fail-closed error")
	}
}

func TestSetLocalTenantNoScopeIsNoop(t *testing.T) {
	t.Parallel()
	// No scope → returns nil without touching tx (nil tx safe).
	if err := setLocalTenant(context.Background(), nil); err != nil {
		t.Fatalf("setLocalTenant with no scope: err = %v, want nil (noop)", err)
	}
}
