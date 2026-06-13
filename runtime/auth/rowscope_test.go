package auth

// Tests for Principal.RowVisibility — the identity→RowScope narrowing contract
// added in EPIC #1337 PR-5 (#1343).

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/pkg/testutil/slogcapture"
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

type rowVisibilityCase struct {
	name        string
	principal   *Principal
	wantScope   tenant.RowScope
	wantSubject string
	wantErr     bool
}

type superAdminAuditCase struct {
	name      string
	principal *Principal
	wantError bool // true = expect ≥1 Error-level record with actor+scope+tenant+reason keys
}

// TestPrincipalRowVisibility_DerivationTable verifies the full derivation table
// for Principal.RowVisibility across all PrincipalKind values and role combos.
//
// Derivation rules:
//
//	nil receiver                        → error (fail-closed)
//	PrincipalUser + HasRole(RoleSuperAdmin) → RowScopeAll,   subject ""
//	PrincipalUser + HasRole(RoleAdmin)      → RowScopeTenant, subject ""
//	PrincipalUser (neither admin)           → RowScopeSelf,   subject = p.Subject
//	PrincipalDevice                         → RowScopeDevice, subject = p.Subject
//	PrincipalService / PrincipalAnonymous / PrincipalUnknown → error
func TestPrincipalRowVisibility_DerivationTable(t *testing.T) {
	ctx := rowVisibilityTestCtx()

	cases := []rowVisibilityCase{
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
			// A device principal must be minted through the sealed issuer; a bare
			// Principal{Kind: PrincipalDevice} literal lacks the device seal and is
			// rejected by RowVisibility (see TestDevicePrincipal_Forged...).
			name:        "device_principal",
			principal:   mustMintDevice(t, "device-xyz"),
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
		{
			// RowScopeSelf requires a non-empty subject; PrincipalUser with empty
			// Subject must propagate the NewRowVisibility validation error.
			name: "non_admin_user_empty_subject_fail_closed",
			principal: &Principal{
				Kind:    PrincipalUser,
				Subject: "",
				Roles:   nil,
			},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertPrincipalRowVisibility(t, ctx, tc)
		})
	}
}

func assertPrincipalRowVisibility(t *testing.T, ctx context.Context, tc rowVisibilityCase) {
	t.Helper()

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
}

// TestPrincipalRowVisibility_SuperAdminMandatoryAudit verifies that the
// RowVisibility derivation path for super-admin emits a mandatory slog.Error
// security audit event BEFORE constructing the RowScopeAll obligation.
//
// The security audit is mandatory — any super-admin cross-tenant access must be
// observable without querying the audit ledger (which the super-admin can
// themselves read). Omitting the log would create an unobservable privilege path.
//
// Keys required on the slog.Error record (FR-007):
//
//	"actor"  = p.Subject
//	"scope"  = "all"
//	"tenant" = p.TenantID
//	"reason" = "cross_tenant_read"
func TestPrincipalRowVisibility_SuperAdminMandatoryAudit(t *testing.T) {
	capture := &captureHandler{}
	slogcapture.InstallDefault(t, slog.New(capture))

	ctx := rowVisibilityTestCtx()

	cases := []superAdminAuditCase{
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
			// Super-admin with empty TenantID (e.g. system-bootstrap context) must
			// still emit the mandatory FR-007 audit; the audit is unconditional on
			// the super-admin path regardless of whether TenantID is set.
			name: "superadmin_empty_tenant_still_audited",
			principal: &Principal{
				Kind:     PrincipalUser,
				Subject:  "super-bootstrap",
				Roles:    []string{RoleSuperAdmin},
				TenantID: "", // empty — audit must still fire
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
			name:      "device_no_audit_log",
			principal: mustMintDevice(t, "device-d1"),
			wantError: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSuperAdminAudit(t, ctx, capture, tc)
		})
	}
}

func assertSuperAdminAudit(t *testing.T, ctx context.Context, capture *captureHandler, tc superAdminAuditCase) {
	t.Helper()

	capture.records = capture.records[:0] // reset between sub-tests

	_, _ = tc.principal.RowVisibility(ctx) // only testing slog side-effect

	errorCount := countMandatoryAuditRecords(capture.records)
	if tc.wantError && errorCount == 0 {
		t.Errorf("%s: expected ≥1 slog.Error with keys {actor,scope,tenant,reason}, got 0 matching records (total records: %d)",
			tc.name, len(capture.records))
	}
	if !tc.wantError && errorCount > 0 {
		t.Errorf("%s: expected no slog.Error audit records, got %d", tc.name, errorCount)
	}
}

func countMandatoryAuditRecords(records []slog.Record) int {
	errorCount := 0
	for _, r := range records {
		if r.Level != slog.LevelError {
			continue
		}
		if hasMandatoryAuditAttrs(r) {
			errorCount++
		}
	}
	return errorCount
}

func hasMandatoryAuditAttrs(r slog.Record) bool {
	hasActor, hasScope, hasTenant, hasReason := false, false, false, false
	r.Attrs(func(a slog.Attr) bool {
		switch a.Key {
		case "actor":
			hasActor = true
		case "scope":
			hasScope = true
		case "tenant":
			hasTenant = true
		case "reason":
			hasReason = true
		}
		return true
	})
	return hasActor && hasScope && hasTenant && hasReason
}
