package auth

// INVARIANT: ROWSCOPEALL-AUDIT-FUNNEL-01
//
// RowVisibility is the PR-5 identity→RowScope derivation referenced by
// pkg/authz/obligation.go (the future PR-11/12 obligation combiner).
//
// ROWSCOPEALL-AUDIT-FUNNEL-01: tenant.NewRowVisibility(RowScopeAll, ...) is the
// sole sanctioned producer of the RowScopeAll obligation; it must be co-located
// with the mandatory slog.Error audit event in this function body. Any other
// call-site constructing RowScopeAll in the production tree is a violation.
//
// FR-007 mandatory audit: every super-admin cross-tenant access (RowScopeAll)
// MUST emit a slog.Error security event carrying actor, scope, and tenant fields
// BEFORE the obligation is constructed. This ensures RowScopeAll access is
// observable without querying the audit ledger, which the super-admin could read
// themselves.
//
// Device and super-admin principals are type-foundation entries: no production
// token issuer on develop assigns these kinds/roles yet (see PrincipalDevice
// godoc and RoleSuperAdmin godoc), but the derivation rules are established here
// for test-injection and downstream ABAC integration (PR-11/12).

import (
	"context"
	"log/slog"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
)

// RowVisibility derives the row-visibility obligation for a request principal.
// The derivation table is:
//
//   - nil receiver                              → KindInternal error (programmer error)
//   - PrincipalUser + HasRole(RoleSuperAdmin)   → RowScopeAll,    subject ""  (+ mandatory audit)
//   - PrincipalUser + HasRole(RoleAdmin)         → RowScopeTenant, subject ""
//   - PrincipalUser (neither admin role)         → RowScopeSelf,   subject = p.Subject
//   - PrincipalDevice                            → RowScopeDevice, subject = p.Subject
//   - PrincipalService / PrincipalAnonymous / PrincipalUnknown → KindPermissionDenied error
//
// Super-admin is checked before admin: a principal holding both roles derives
// RowScopeAll (super-admin wins).
//
// This is the ONLY sanctioned constructor of RowScopeAll obligations
// (ROWSCOPEALL-AUDIT-FUNNEL-01). The super-admin path emits a mandatory
// slog.Error audit event (FR-007) before constructing the obligation.
func (p *Principal) RowVisibility(ctx context.Context) (tenant.RowVisibility, error) {
	if p == nil {
		return tenant.RowVisibility{}, errcode.New(
			errcode.KindInternal,
			errcode.ErrInternal,
			"RowVisibility called on nil principal",
		)
	}

	switch p.Kind {
	case PrincipalUser:
		return deriveUserRowVisibility(ctx, p)
	case PrincipalDevice:
		return tenant.NewRowVisibility(tenant.RowScopeDevice, p.Subject)
	case PrincipalService, PrincipalAnonymous, PrincipalUnknown:
		return tenant.RowVisibility{}, errcode.New(
			errcode.KindPermissionDenied,
			errcode.ErrAuthForbidden,
			"principal kind cannot establish a row-visibility scope",
		)
	}
	// Unreachable: the switch above covers all five PrincipalKind constants.
	// This satisfies PRINCIPAL-KIND-EXHAUSTIVE-SWITCH-01 — the default branch
	// is absent, so a new PrincipalKind constant added without a case here will
	// be caught by archtest PRINCIPAL-KIND-EXHAUSTIVE-SWITCH-01 (Medium, nightly
	// archtest — not compile-time).
	return tenant.RowVisibility{}, errcode.New(
		errcode.KindInternal,
		errcode.ErrInternal,
		"unhandled principal kind in RowVisibility",
	)
}

// deriveUserRowVisibility is extracted from RowVisibility to keep cognitive
// complexity within the ≤15 ceiling (CLAUDE.md).
func deriveUserRowVisibility(ctx context.Context, p *Principal) (tenant.RowVisibility, error) {
	if p.HasRole(RoleSuperAdmin) {
		// FR-007: mandatory cross-tenant audit — must emit BEFORE constructing
		// the RowScopeAll obligation. See ROWSCOPEALL-AUDIT-FUNNEL-01.
		slog.ErrorContext(ctx, "super-admin cross-tenant row visibility granted",
			slog.String("actor", p.Subject),
			slog.String("scope", "all"),
			slog.String("tenant", p.TenantID),
			slog.String("reason", "cross_tenant_read"),
		)
		return tenant.NewRowVisibility(tenant.RowScopeAll, "")
	}
	if p.HasRole(RoleAdmin) {
		return tenant.NewRowVisibility(tenant.RowScopeTenant, "")
	}
	return tenant.NewRowVisibility(tenant.RowScopeSelf, p.Subject)
}
