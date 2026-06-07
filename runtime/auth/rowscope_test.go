package auth

// TestPrincipalRowVisibility_DerivationTable and
// TestPrincipalRowVisibility_SuperAdminMandatoryAudit are RED TDD tests for
// Principal.RowVisibility — the identity→RowScope narrowing contract added in
// EPIC #1337 PR-5 (#1343).
//
// These tests reference:
//   - p.RowVisibility(ctx) — method that does NOT exist yet
//   - RoleSuperAdmin         — const that does NOT exist yet
//
// Both references cause a compilation failure, which is the intended RED state.
// The tests will turn GREEN once the implementation lands.

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/pkg/tenant"
)

// rowVisibilityTestCtx is a minimal derivation-only context for RowVisibility
// tests; no tenant ctxkey is needed because RowVisibility reads from the
// receiver, not the context (the ctx is threaded for future audit-log injection).
func rowVisibilityTestCtx() context.Context { return context.Background() }

// captureHandler is a minimal slog.Handler that accumulates every log record.
// We MUST NOT use slog.NewJSONHandler or slog.NewTextHandler here — both are
// banned by archtest SLOG-HANDLER-SEALED-FUNNEL-01 outside the logging package.
type captureHandler struct {
	records []slog.Record
}

func (h *captureHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }
func (h *captureHandler) Handle(_ context.Context, r slog.Record) error {
	h.records = append(h.records, r)
	return nil
}
func (h *captureHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *captureHandler) WithGroup(_ string) slog.Handler      { return h }

// TestPrincipalRowVisibility_DerivationTable verifies the full derivation table
// for Principal.RowVisibility across all PrincipalKind values and role combos.
//
// Contract (non-existent symbols → compile failure = RED):
//
//	nil receiver                        → error (fail-closed)
//	PrincipalUser + HasRole(RoleSuperAdmin) → RowScopeAll,   subject ""
//	PrincipalUser + HasRole(RoleAdmin)      → RowScopeTenant, subject ""
//	PrincipalUser (neither admin)           → RowScopeSelf,   subject = p.Subject
//	PrincipalDevice                         → RowScopeDevice, subject = p.Subject
//	PrincipalService / PrincipalAnonymous / PrincipalUnknown → error
func TestPrincipalRowVisibility_DerivationTable(t *testing.T) {
	ctx := rowVisibilityTestCtx()

	cases := []struct {
		name        string
		principal   *Principal
		wantScope   tenant.RowScope
		wantSubject string
		wantErr     bool
	}{
		{
			name:      "nil_receiver_fail_closed",
			principal: nil,
			wantErr:   true,
		},
		{
			name: "superadmin_precedence_over_admin",
			principal: &Principal{
				Kind:    PrincipalUser,
				Subject: "super-usr",
				Roles:   []string{RoleSuperAdmin, RoleAdmin}, // super-admin wins
			},
			wantScope:   tenant.RowScopeAll,
			wantSubject: "",
		},
		{
			name: "superadmin_only",
			principal: &Principal{
				Kind:    PrincipalUser,
				Subject: "super-only",
				Roles:   []string{RoleSuperAdmin},
			},
			wantScope:   tenant.RowScopeAll,
			wantSubject: "",
		},
		{
			name: "admin_tenant_scope",
			principal: &Principal{
				Kind:    PrincipalUser,
				Subject: "admin-usr",
				Roles:   []string{RoleAdmin},
			},
			wantScope:   tenant.RowScopeTenant,
			wantSubject: "",
		},
		{
			name: "non_admin_self_scope",
			principal: &Principal{
				Kind:    PrincipalUser,
				Subject: "plain-usr",
				Roles:   nil,
			},
			wantScope:   tenant.RowScopeSelf,
			wantSubject: "plain-usr",
		},
		{
			name: "viewer_role_non_admin_self_scope",
			principal: &Principal{
				Kind:    PrincipalUser,
				Subject: "viewer-usr",
				Roles:   []string{"viewer"},
			},
			wantScope:   tenant.RowScopeSelf,
			wantSubject: "viewer-usr",
		},
		{
			name: "device_principal",
			principal: &Principal{
				Kind:    PrincipalDevice,
				Subject: "device-xyz",
			},
			wantScope:   tenant.RowScopeDevice,
			wantSubject: "device-xyz",
		},
		{
			name: "service_principal_fail_closed",
			principal: &Principal{
				Kind:    PrincipalService,
				Subject: "svc-cell",
			},
			wantErr: true,
		},
		{
			name: "anonymous_principal_fail_closed",
			principal: &Principal{
				Kind:    PrincipalAnonymous,
				Subject: "",
			},
			wantErr: true,
		},
		{
			name: "unknown_principal_fail_closed",
			principal: &Principal{
				Kind:    PrincipalUnknown,
				Subject: "",
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// RowVisibility does not exist yet — this call is the RED trigger.
			vis, err := tc.principal.RowVisibility(ctx)
			if tc.wantErr {
				if err == nil {
					t.Errorf("RowVisibility(%s): expected error, got visibility %v", tc.name, vis)
				}
				return
			}
			if err != nil {
				t.Fatalf("RowVisibility(%s): unexpected error: %v", tc.name, err)
			}
			if vis.Scope() != tc.wantScope {
				t.Errorf("RowVisibility(%s): scope = %v, want %v", tc.name, vis.Scope(), tc.wantScope)
			}
			if vis.Subject() != tc.wantSubject {
				t.Errorf("RowVisibility(%s): subject = %q, want %q", tc.name, vis.Subject(), tc.wantSubject)
			}
		})
	}
}

// TestPrincipalRowVisibility_SuperAdminMandatoryAudit verifies that the
// RowVisibility derivation path for super-admin emits a mandatory slog.Error
// security audit event BEFORE constructing the RowScopeAll obligation.
//
// The security audit is mandatory — any super-admin cross-tenant access must be
// observable without querying the audit ledger (which the super-admin can
// themselves read). Omitting the log would create an unobservable privilege path.
//
// Keys required on the slog.Error record:
//
//	"actor"  = p.Subject
//	"scope"  = "all"
//	"tenant" = p.TenantID
func TestPrincipalRowVisibility_SuperAdminMandatoryAudit(t *testing.T) {
	capture := &captureHandler{}
	prev := slog.Default()
	slog.SetDefault(slog.New(capture))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ctx := rowVisibilityTestCtx()

	cases := []struct {
		name      string
		principal *Principal
		wantError bool // true = expect ≥1 Error-level record with actor+scope+tenant keys
	}{
		{
			name: "superadmin_emits_mandatory_audit",
			principal: &Principal{
				Kind:     PrincipalUser,
				Subject:  "super-alice",
				Roles:    []string{RoleSuperAdmin},
				TenantID: "acme-tenant-id",
			},
			wantError: true,
		},
		{
			name: "admin_no_audit_log",
			principal: &Principal{
				Kind:    PrincipalUser,
				Subject: "admin-bob",
				Roles:   []string{RoleAdmin},
			},
			wantError: false,
		},
		{
			name: "non_admin_no_audit_log",
			principal: &Principal{
				Kind:    PrincipalUser,
				Subject: "plain-carol",
				Roles:   nil,
			},
			wantError: false,
		},
		{
			name: "device_no_audit_log",
			principal: &Principal{
				Kind:    PrincipalDevice,
				Subject: "device-d1",
			},
			wantError: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			capture.records = capture.records[:0] // reset between sub-tests

			// RowVisibility does not exist yet — RED trigger.
			_, _ = tc.principal.RowVisibility(ctx) // only testing slog side-effect

			// Count Error-level records with the required keys.
			errorCount := 0
			for _, r := range capture.records {
				if r.Level != slog.LevelError {
					continue
				}
				hasActor, hasScope, hasTenant := false, false, false
				r.Attrs(func(a slog.Attr) bool {
					switch a.Key {
					case "actor":
						hasActor = true
					case "scope":
						hasScope = true
					case "tenant":
						hasTenant = true
					}
					return true
				})
				if hasActor && hasScope && hasTenant {
					errorCount++
				}
			}

			if tc.wantError && errorCount == 0 {
				t.Errorf("%s: expected ≥1 slog.Error with keys {actor,scope,tenant}, got 0 matching records (total records: %d)",
					tc.name, len(capture.records))
			}
			if !tc.wantError && errorCount > 0 {
				t.Errorf("%s: expected no slog.Error audit records, got %d", tc.name, errorCount)
			}
		})
	}
}
