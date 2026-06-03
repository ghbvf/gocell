package tenant

import (
	"context"
	"fmt"

	"github.com/ghbvf/gocell/pkg/ctxkeys"
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
// Pre-authentication paths that have no ctx tenant (e.g. sessionlogin, which
// derives the tenant from the request body's TenantID, and sessionrefresh, which
// reads users by the trusted global UUID primary key) do NOT use FromContext;
// they obtain the TenantID from their own request-scoped source or use the
// by-PK tenant-deriving read carve-out (UserRepository.GetByID).
func FromContext(ctx context.Context) (TenantID, error) {
	raw, ok := ctxkeys.TenantIDFrom(ctx)
	if !ok {
		return "", fmt.Errorf("tenant: no tenant in context (auth boundary did not populate ctxkeys.TenantID)")
	}
	tid, err := ParseTenantID(raw)
	if err != nil {
		return "", fmt.Errorf("tenant: tenant in context is invalid: %w", err)
	}
	return tid, nil
}
