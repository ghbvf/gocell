package tenant

import (
	"context"

	"github.com/ghbvf/gocell/framework/pkg/ctxkeys"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
)

// FromContext is the fail-closed read-side bridge between the ctx tenant key
// (written only at the trusted auth boundary — runtime/auth/middleware.go's
// injectPrincipalCtxKeys, or the consumer-side outbox restore, per
// CTXKEYS-PRINCIPAL-WRITE-CALLER-01) and the TenantID typed positional
// parameter that tenant-scoped repo methods require.
//
// It is the single sanctioned source a tenant-scoped service callsite uses to
// obtain the TenantID it must pass to the repo layer. Post-auth handlers and
// event consumers (after RestoreToContext) call it once and thread the result
// into the repo method.
//
// FromContext fails closed: a missing key OR an empty / non-canonical value is
// an error, never a silent zero value. A tenant-scoped query reaching the repo
// without a tenant is a bug (the auth bridge must have populated the key for any
// tenant-bearing principal), so refusing here keeps the "no tenant predicate"
// failure mode impossible to express by accident. ParseTenantID re-validates
// (and canonicalizes) defensively even though the auth bridge already did so.
//
// INVARIANT (#1882 / #1883): this fail-closed empty/nil rejection is the Hard layer
// that makes "empty ctx tenant ⟹ cross-tenant authority" unexpressible on the
// sanctioned path. A saga-journal projection replays under the system principal with
// an empty tenant (kernel/projection.InstallSystemPrincipal; ADR #1609 §5); a handler
// deriving scope via FromContext therefore gets a 403, never a footgun "". The raw
// reader ctxkeys.TenantIDFrom is the only path around this and is pinned to an infra
// allowlist by CTXKEYS-TENANT-READ-CALLER-01.
//
// CLASSIFICATION — the failure-path error is a typed *errcode.Error classified
// as KindPermissionDenied (HTTP 403 Forbidden), single-sourced here so every
// tenant-scoped handler maps a missing/invalid tenant to a 403 instead of a
// 500. The semantics are "authenticated principal lacks a tenant scope": the
// caller is authenticated (the auth boundary ran) but presents no usable
// tenant predicate, which is an authorization failure, not an internal fault.
// The returned value is still an `error`, so non-HTTP callers (accesscore
// services, event consumers) are unaffected; HTTP cell adapters inspect the
// code via errors.As and return the generated typed 4xx response struct. The
// ParseTenantID cause is carried (errcode.Wrap) for server-side logging /
// errors.Is chains but is stripped from the wire on the 4xx envelope.
//
// Pre-authentication paths that have no ctx tenant (e.g. sessionlogin, which
// derives the tenant from the request body's TenantID, and sessionrefresh, which
// reads users by the trusted global UUID primary key) do NOT use FromContext;
// they obtain the TenantID from their own request-scoped source or use the
// by-PK tenant-deriving read carve-out (UserRepository.GetByID).
func FromContext(ctx context.Context) (TenantID, error) {
	raw, ok := ctxkeys.TenantIDFrom(ctx)
	if !ok {
		return "", errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden,
			"tenant scope required")
	}
	tid, err := ParseTenantID(raw)
	if err != nil {
		return "", errcode.Wrap(errcode.KindPermissionDenied, errcode.ErrAuthForbidden,
			"tenant scope invalid", err)
	}
	return tid, nil
}
