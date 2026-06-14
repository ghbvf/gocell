package ordercell

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/domain"
	dto "github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/dto"
	"github.com/ghbvf/gocell/examples/todoorder/cells/ordercell/internal/mem"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// TestOrderAuthorizer exercises every action branch of orderAuthorizer.Authorize.
//
// owner != path id: an order's Owner is the JWT subject who created it (set by
// ordercreate.Service.Create), not the order ID. The authorizer performs a PIP
// lookup (repo.GetByID(resource)) to fetch order.Owner and compare with subject.
func TestOrderAuthorizer(t *testing.T) {
	const (
		customerA = "user-customer-a"
		customerB = "user-customer-b"
		orderID   = "ord-test-001"
	)

	// Seed a repo with one order whose owner is customerA.
	repo := mem.NewOrderRepository()
	err := repo.Create(context.Background(), &domain.Order{
		ID:     orderID,
		Item:   "widget",
		Status: "pending",
		Owner:  customerA,
	})
	if err != nil {
		t.Fatalf("seed repo: %v", err)
	}

	cases := []struct {
		name      string
		ctx       context.Context
		subject   string
		resource  string
		action    string
		wantAllow bool
	}{
		// ── order:create ──────────────────────────────────────────────────────────
		{
			name:      "create: customer → allow",
			ctx:       auth.TestContext(customerA, []string{dto.RoleCustomer}),
			subject:   customerA,
			resource:  "",
			action:    authz.PermOrderCreate().String(),
			wantAllow: true,
		},
		{
			name:      "create: non-customer → deny",
			ctx:       auth.TestContext(customerA, []string{"viewer"}),
			subject:   customerA,
			resource:  "",
			action:    authz.PermOrderCreate().String(),
			wantAllow: false,
		},
		{
			name:      "create: no principal → deny",
			ctx:       context.Background(),
			subject:   "",
			resource:  "",
			action:    authz.PermOrderCreate().String(),
			wantAllow: false,
		},

		// ── order:list ────────────────────────────────────────────────────────────
		{
			name:      "list: customer → allow",
			ctx:       auth.TestContext(customerA, []string{dto.RoleCustomer}),
			subject:   customerA,
			resource:  "",
			action:    authz.PermOrderList().String(),
			wantAllow: true,
		},
		{
			name:      "list: non-customer → deny",
			ctx:       auth.TestContext(customerA, []string{"viewer"}),
			subject:   customerA,
			resource:  "",
			action:    authz.PermOrderList().String(),
			wantAllow: false,
		},
		{
			name:      "list: no principal → deny",
			ctx:       context.Background(),
			subject:   "",
			resource:  "",
			action:    authz.PermOrderList().String(),
			wantAllow: false,
		},

		// ── order:read (owner-scoped, PIP lookup) ─────────────────────────────────
		{
			name:      "read: owner (subject==order.Owner) → allow",
			ctx:       auth.TestContext(customerA, []string{dto.RoleCustomer}),
			subject:   customerA,
			resource:  orderID,
			action:    authz.PermOrderRead().String(),
			wantAllow: true,
		},
		{
			name:      "read: non-owner (subject!=order.Owner) → deny",
			ctx:       auth.TestContext(customerB, []string{dto.RoleCustomer}),
			subject:   customerB,
			resource:  orderID,
			action:    authz.PermOrderRead().String(),
			wantAllow: false,
		},
		{
			name:      "read: order not found → deny (fail-closed)",
			ctx:       auth.TestContext(customerA, []string{dto.RoleCustomer}),
			subject:   customerA,
			resource:  "ord-does-not-exist",
			action:    authz.PermOrderRead().String(),
			wantAllow: false,
		},
		{
			name:      "read: empty subject → deny",
			ctx:       context.Background(),
			subject:   "",
			resource:  orderID,
			action:    authz.PermOrderRead().String(),
			wantAllow: false,
		},

		// ── order:update (owner-scoped, PIP lookup) ───────────────────────────────
		{
			name:      "update: owner (subject==order.Owner) → allow",
			ctx:       auth.TestContext(customerA, []string{dto.RoleCustomer}),
			subject:   customerA,
			resource:  orderID,
			action:    authz.PermOrderUpdate().String(),
			wantAllow: true,
		},
		{
			name:      "update: non-owner (subject!=order.Owner) → deny",
			ctx:       auth.TestContext(customerB, []string{dto.RoleCustomer}),
			subject:   customerB,
			resource:  orderID,
			action:    authz.PermOrderUpdate().String(),
			wantAllow: false,
		},
		{
			name:      "update: order not found → deny (fail-closed)",
			ctx:       auth.TestContext(customerA, []string{dto.RoleCustomer}),
			subject:   customerA,
			resource:  "ord-does-not-exist",
			action:    authz.PermOrderUpdate().String(),
			wantAllow: false,
		},
		{
			name:      "update: empty subject → deny",
			ctx:       context.Background(),
			subject:   "",
			resource:  orderID,
			action:    authz.PermOrderUpdate().String(),
			wantAllow: false,
		},

		// ── unknown action ────────────────────────────────────────────────────────
		{
			name:      "unknown action → deny",
			ctx:       auth.TestContext(customerA, []string{dto.RoleCustomer}),
			subject:   customerA,
			resource:  "",
			action:    "unknown:action",
			wantAllow: false,
		},
	}

	az := newOrderAuthorizer(repo)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dec, err := az.Authorize(tc.ctx, tc.subject, tc.resource, tc.action)
			if err != nil {
				t.Fatalf("Authorize returned unexpected error: %v", err)
			}
			if got := dec.IsAllow(); got != tc.wantAllow {
				t.Errorf("IsAllow() = %v, want %v (reason: %q)", got, tc.wantAllow, dec.Reason())
			}
		})
	}
}

// TestOrderAuthorizer_Interface confirms the compile-time interface assertion
// embedded in authorizer.go still holds after any refactor.
func TestOrderAuthorizer_Interface(t *testing.T) {
	var _ auth.Authorizer = orderAuthorizer{}
}

// TestOrderCell_AuthorizerMethod verifies that OrderCell.Authorizer() is non-nil
// after Init (satisfying bootstrap.PrimaryAuthorizerOption discovery).
func TestOrderCell_AuthorizerMethod(t *testing.T) {
	c := newTestCell()
	if err := c.Init(context.Background(), newTestRec()); err != nil {
		t.Fatalf("Init: %v", err)
	}
	if got := c.Authorizer(); got == nil {
		t.Error("OrderCell.Authorizer() must return non-nil after Init")
	}
}
