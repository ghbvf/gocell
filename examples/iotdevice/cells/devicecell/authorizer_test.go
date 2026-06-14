package devicecell

import (
	"context"
	"testing"

	dto "github.com/ghbvf/gocell/examples/iotdevice/cells/devicecell/internal/dto"
	"github.com/ghbvf/gocell/framework/pkg/authz"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// TestDeviceAuthorizer exercises every action branch of deviceAuthorizer.Authorize.
//
// Table schema: subject and resource are the Authorize call args; roles are
// injected into the principal via auth.TestContext; wantAllow is the expected
// outcome. The test also covers the no-principal path (nil ctx) and unknown
// action → deny.
func TestDeviceAuthorizer(t *testing.T) {
	const (
		deviceA  = "device-a"
		deviceB  = "device-b"
		operID   = "operator-1"
		adminID  = "admin-1"
		viewerID = "viewer-1"
	)

	cases := []struct {
		name      string
		ctx       context.Context
		subject   string
		resource  string
		action    string
		wantAllow bool
	}{
		// ── device:command ─────────────────────────────────────────────────────
		{
			name:      "command: operator → allow",
			ctx:       auth.TestContext(operID, []string{dto.RoleOperator}),
			subject:   operID,
			resource:  deviceA,
			action:    authz.PermDeviceCommand().String(),
			wantAllow: true,
		},
		{
			name:      "command: admin → allow",
			ctx:       auth.TestContext(adminID, []string{dto.RoleAdmin}),
			subject:   adminID,
			resource:  deviceA,
			action:    authz.PermDeviceCommand().String(),
			wantAllow: true,
		},
		{
			name:      "command: device-only role → deny",
			ctx:       auth.TestContext(deviceA, []string{dto.RoleDevice}),
			subject:   deviceA,
			resource:  deviceA,
			action:    authz.PermDeviceCommand().String(),
			wantAllow: false,
		},
		{
			name:      "command: no principal → deny",
			ctx:       context.Background(),
			subject:   "",
			resource:  deviceA,
			action:    authz.PermDeviceCommand().String(),
			wantAllow: false,
		},

		// ── device:consume ────────────────────────────────────────────────────
		{
			name:      "consume: owner (subject==resource) → allow",
			ctx:       auth.TestContext(deviceA, nil),
			subject:   deviceA,
			resource:  deviceA,
			action:    authz.PermDeviceConsume().String(),
			wantAllow: true,
		},
		{
			name:      "consume: operator → allow",
			ctx:       auth.TestContext(operID, []string{dto.RoleOperator}),
			subject:   operID,
			resource:  deviceA,
			action:    authz.PermDeviceConsume().String(),
			wantAllow: true,
		},
		{
			name:      "consume: non-owner device (subject!=resource) → deny",
			ctx:       auth.TestContext(deviceB, []string{dto.RoleDevice}),
			subject:   deviceB,
			resource:  deviceA,
			action:    authz.PermDeviceConsume().String(),
			wantAllow: false,
		},
		{
			name:      "consume: empty subject → deny",
			ctx:       context.Background(),
			subject:   "",
			resource:  deviceA,
			action:    authz.PermDeviceConsume().String(),
			wantAllow: false,
		},

		// ── device:read ───────────────────────────────────────────────────────
		{
			name:      "read: owner (subject==resource) → allow",
			ctx:       auth.TestContext(deviceA, nil),
			subject:   deviceA,
			resource:  deviceA,
			action:    authz.PermDeviceRead().String(),
			wantAllow: true,
		},
		{
			name:      "read: admin → allow",
			ctx:       auth.TestContext(adminID, []string{dto.RoleAdmin}),
			subject:   adminID,
			resource:  deviceA,
			action:    authz.PermDeviceRead().String(),
			wantAllow: true,
		},
		{
			name:      "read: operator → allow",
			ctx:       auth.TestContext(operID, []string{dto.RoleOperator}),
			subject:   operID,
			resource:  deviceA,
			action:    authz.PermDeviceRead().String(),
			wantAllow: true,
		},
		{
			name:      "read: non-owner device → deny",
			ctx:       auth.TestContext(deviceB, []string{dto.RoleDevice}),
			subject:   deviceB,
			resource:  deviceA,
			action:    authz.PermDeviceRead().String(),
			wantAllow: false,
		},
		{
			name:      "read: empty subject → deny",
			ctx:       context.Background(),
			subject:   "",
			resource:  deviceA,
			action:    authz.PermDeviceRead().String(),
			wantAllow: false,
		},

		// ── device:list ───────────────────────────────────────────────────────
		{
			name:      "list: admin → allow",
			ctx:       auth.TestContext(adminID, []string{dto.RoleAdmin}),
			subject:   adminID,
			resource:  "",
			action:    authz.PermDeviceList().String(),
			wantAllow: true,
		},
		{
			name:      "list: operator → deny",
			ctx:       auth.TestContext(operID, []string{dto.RoleOperator}),
			subject:   operID,
			resource:  "",
			action:    authz.PermDeviceList().String(),
			wantAllow: false,
		},
		{
			name:      "list: viewer → deny",
			ctx:       auth.TestContext(viewerID, []string{"viewer"}),
			subject:   viewerID,
			resource:  "",
			action:    authz.PermDeviceList().String(),
			wantAllow: false,
		},

		// ── unknown action ────────────────────────────────────────────────────
		{
			name:      "unknown action → deny",
			ctx:       auth.TestContext(adminID, []string{dto.RoleAdmin}),
			subject:   adminID,
			resource:  "",
			action:    "unknown:action",
			wantAllow: false,
		},
	}

	az := deviceAuthorizer{}
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

// TestDeviceAuthorizer_Interface confirms the compile-time interface assertion
// embedded in authorizer.go still holds after any refactor.
func TestDeviceAuthorizer_Interface(t *testing.T) {
	var _ auth.Authorizer = deviceAuthorizer{}
}
