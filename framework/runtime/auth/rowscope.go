package auth

// INVARIANT: ROWSCOPEALL-AUDIT-FUNNEL-01
//
// RowVisibility is the PR-5 identity→RowScope derivation referenced by
// pkg/authz/obligation.go (the future PR-11/12 obligation combiner).
//
// ROWSCOPEALL-AUDIT-FUNNEL-01: tenant.NewCrossTenantVisibility() is the sole
// sanctioned producer of the RowScopeAll obligation (#1760: tenant.NewRowVisibility
// now REJECTS RowScopeAll, so the general path is provably incapable of minting
// it). The single mint lives in (*Principal).CrossTenantVisibility below, co-located
// with the mandatory slog.Error audit event in the same function body. Any other
// call-site constructing RowScopeAll in the production tree is a violation.
//
// FR-007 mandatory audit: every super-admin cross-tenant access (RowScopeAll)
// MUST emit a slog.Error security event carrying actor, scope, and tenant fields
// BEFORE the obligation is constructed. Because the audit is co-located with the
// SOLE mint (CrossTenantVisibility), every cross-tenant obligation is audited
// regardless of which caller triggers it — RowVisibility's super-admin branch
// DELEGATES to CrossTenantVisibility rather than minting independently. This
// ensures RowScopeAll access is observable without querying the audit ledger,
// which the super-admin could read themselves.
//
// Production issuers (#1898): a device principal is minted by mintDevicePrincipal
// (deviceprincipal.go) from a verified device bearer token and carries a sealed
// Principal.device proof — the device branch below requires that seal, so a
// forged Principal{Kind: PrincipalDevice} is type-inert (fail-closed). A
// super-admin principal is minted by the ordinary user path whenever the JWT
// "roles" claim carries RoleSuperAdmin (sessionmint.MintAccess signs the
// subject's stored roles with no role allowlist), so the RowScopeAll derivation
// + mandatory FR-007 audit below are on the live production path.

import (
	"context"
	"log/slog"

	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/tenant"
)

// RowVisibility derives the row-visibility obligation for a request principal.
// The derivation table is:
//
//   - nil receiver                              → KindInternal error (programmer error)
//   - PrincipalUser + HasRole(RoleSuperAdmin)   → RowScopeAll,    subject ""  (+ mandatory audit)
//   - PrincipalUser + HasRole(RoleAdmin)         → RowScopeTenant, subject ""
//   - PrincipalUser (neither admin role)         → RowScopeSelf,   subject = p.Subject
//   - PrincipalDevice (with issuer seal)         → RowScopeDevice, subject = p.Subject
//   - PrincipalDevice (no seal, i.e. forged)     → KindPermissionDenied error
//   - PrincipalService / PrincipalAnonymous / PrincipalUnknown → KindPermissionDenied error
//
// Super-admin is checked before admin: a principal holding both roles derives
// RowScopeAll (super-admin wins).
//
// The super-admin branch DELEGATES to CrossTenantVisibility (the sole sanctioned
// RowScopeAll mint, ROWSCOPEALL-AUDIT-FUNNEL-01), which emits the mandatory
// FR-007 slog.Error audit before constructing the obligation and returns the
// sealed value unwrapped via Visibility(). A consumer that needs the sealed
// tenant.CrossTenantVisibility itself (the #1810 cross-tenant audit read) calls
// CrossTenantVisibility directly.
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
		if p.device == nil {
			// A Principal{Kind: PrincipalDevice} that did not come from
			// mintDevicePrincipal lacks the device seal — fail closed and never
			// derive a device row scope from an unsanctioned (forged) principal.
			// This is the consuming half of DEVICE-PRINCIPAL-MINT-CALLER-01.
			return tenant.RowVisibility{}, errcode.New(
				errcode.KindPermissionDenied,
				errcode.ErrAuthForbidden,
				"device principal missing issuer seal",
			)
		}
		return tenant.NewRowVisibility(tenant.RowScopeDevice, p.Subject)
	case PrincipalService, PrincipalAnonymous, PrincipalUnknown:
		return tenant.RowVisibility{}, errcode.New(
			errcode.KindPermissionDenied,
			errcode.ErrAuthForbidden,
			"principal kind cannot establish a row-visibility scope",
		)
	}
	// Unreachable: the switch above covers all five PrincipalKind constants.
	// PRINCIPAL-KIND-EXHAUSTIVE-SWITCH-01 (tools/archtest/principal_kind_exhaustive_switch_test.go)
	// requires every switch on PrincipalKind in the production tree to have an
	// explicit case for each defined constant. "Explicit" means listed in at
	// least one case arm — merged cases (e.g. "case A, B, C:") count as covered
	// for each value they name; it is only a PrincipalKind constant with NO case
	// arm anywhere in the switch that triggers a diagnostic. A new PrincipalKind
	// added to kernel/auth without a corresponding case here is caught at Medium
	// (nightly archtest), not at compile time.
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
		// Delegate to the sole sanctioned RowScopeAll mint (which emits the
		// mandatory FR-007 audit co-located with the construction); unwrap the
		// sealed value for the plain-RowVisibility return contract.
		ctv, err := p.CrossTenantVisibility(ctx)
		if err != nil {
			return tenant.RowVisibility{}, err
		}
		return ctv.Visibility(), nil
	}
	if p.HasRole(RoleAdmin) {
		return tenant.NewRowVisibility(tenant.RowScopeTenant, "")
	}
	return tenant.NewRowVisibility(tenant.RowScopeSelf, p.Subject)
}

// CrossTenantVisibility derives the sealed cross-tenant (RowScopeAll) obligation
// for a super-admin user principal. It is the SOLE production mint of
// tenant.NewCrossTenantVisibility (ROWSCOPEALL-AUDIT-FUNNEL-01): the mandatory
// FR-007 slog.Error audit is co-located, unconditional, and emitted BEFORE the
// mint, so every cross-tenant obligation — whether reached via this accessor or
// via RowVisibility's delegating super-admin branch — is audited.
//
// It fail-closes (KindPermissionDenied) for any principal that is not a
// super-admin user: the sealed grant is never produced for an admin, normal
// user, device, service, anonymous, or unknown principal. The #1810 cross-tenant
// audit read consumes the returned tenant.CrossTenantVisibility as a Hard typed
// funnel param, so that read is uncallable without routing through this audited
// accessor.
func (p *Principal) CrossTenantVisibility(ctx context.Context) (tenant.CrossTenantVisibility, error) {
	if p == nil {
		return tenant.CrossTenantVisibility{}, errcode.New(
			errcode.KindInternal,
			errcode.ErrInternal,
			"CrossTenantVisibility called on nil principal",
		)
	}
	if p.Kind != PrincipalUser || !p.HasRole(RoleSuperAdmin) {
		return tenant.CrossTenantVisibility{}, errcode.New(
			errcode.KindPermissionDenied,
			errcode.ErrAuthForbidden,
			"cross-tenant visibility requires a super-admin user principal",
		)
	}
	// FR-007: mandatory cross-tenant audit — must emit BEFORE constructing the
	// RowScopeAll obligation. See ROWSCOPEALL-AUDIT-FUNNEL-01.
	slog.ErrorContext(ctx, "super-admin cross-tenant row visibility granted",
		slog.String("actor", p.Subject),
		slog.String("scope", "all"),
		slog.String("tenant", p.TenantID),
		slog.String("reason", "cross_tenant_read"),
	)
	return tenant.NewCrossTenantVisibility(), nil
}
