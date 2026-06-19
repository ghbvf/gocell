package enrollcell

import (
	"context"
	"testing"

	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// TestEnrollAuthorizer exercises every action branch of enrollAuthorizer.Authorize.
func TestEnrollAuthorizer(t *testing.T) {
	const (
		adminID    = "admin-1"
		operatorID = "operator-1"
		userID     = "user-1"
	)

	cases := []struct {
		name      string
		ctx       context.Context
		subject   string
		resource  string
		action    string
		wantAllow bool
	}{
		// ── device:read ───────────────────────────────────────────────────────
		{
			name:      "read: mdm-admin → allow",
			ctx:       auth.TestContext(adminID, []string{RoleMDMAdmin}),
			subject:   adminID,
			resource:  "dev-1",
			action:    authz.PermDeviceRead().String(),
			wantAllow: true,
		},
		{
			name:      "read: mdm-operator → allow",
			ctx:       auth.TestContext(operatorID, []string{RoleMDMOperator}),
			subject:   operatorID,
			resource:  "dev-1",
			action:    authz.PermDeviceRead().String(),
			wantAllow: true,
		},
		{
			name:      "read: no role → deny",
			ctx:       auth.TestContext(userID, []string{}),
			subject:   userID,
			resource:  "dev-1",
			action:    authz.PermDeviceRead().String(),
			wantAllow: false,
		},
		{
			name:      "read: no principal → deny",
			ctx:       context.Background(),
			subject:   "",
			resource:  "dev-1",
			action:    authz.PermDeviceRead().String(),
			wantAllow: false,
		},

		// ── unknown action ────────────────────────────────────────────────────
		{
			name:      "unknown action: admin → deny (closed-set fail-closed)",
			ctx:       auth.TestContext(adminID, []string{RoleMDMAdmin}),
			subject:   adminID,
			resource:  "",
			action:    "unknown:action",
			wantAllow: false,
		},

		// ── nil principal in context ──────────────────────────────────────────
		{
			name:      "nil principal in context → deny",
			ctx:       context.Background(),
			subject:   "",
			resource:  "",
			action:    authz.PermDeviceRead().String(),
			wantAllow: false,
		},
	}

	az := enrollAuthorizer{}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dec, err := az.Authorize(tc.ctx, tc.subject, tc.resource, tc.action)
			if err != nil {
				t.Fatalf("Authorize returned unexpected error: %v", err)
			}
			if got := dec.IsAllow(); got != tc.wantAllow {
				t.Errorf("IsAllow() = %v, want %v", got, tc.wantAllow)
			}
		})
	}
}

// TestEnrollAuthorizer_Interface confirms the compile-time interface assertion.
func TestEnrollAuthorizer_Interface(t *testing.T) {
	var _ auth.Authorizer = enrollAuthorizer{}
}
