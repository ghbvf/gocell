package auth

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/httputil"
)

// RequireSelfOrRole checks that the authenticated subject matches targetID
// or holds one of the specified bypass roles. Returns nil on success.
//
// Deprecated: prefer constructing auth.AnyRole(...) / auth.SelfOr(...) Policy
// and passing it via auth.Route.Policy. The internal Require* helpers below are
// retained only as the implementation backbone.
//
// ref: go-kratos/kratos middleware/auth/auth.go — adopted: subject-from-context
// pattern; deviated: combined self+role check instead of separate authz middleware.
//
// Errors:
//   - ErrAuthUnauthorized: no Principal in context, or PrincipalUser with empty Subject
//   - ErrAuthForbidden: subject does not match targetID and lacks bypass roles
func RequireSelfOrRole(ctx context.Context, targetID string, bypassRoles ...string) error {
	p, ok := FromContext(ctx)
	if !ok {
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "authentication required")
	}
	// G1.B: Defense-in-depth. PrincipalUser must always carry a non-empty Subject;
	// an empty Subject indicates the primary authenticator allowed a malformed token
	// through. PrincipalAnonymous Subject is intentionally empty by design.
	// PrincipalService identity is expressed via CallerCellID, not Subject.
	if p.Kind == PrincipalUser && p.Subject == "" {
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "principal subject missing")
	}

	if targetID == "" {
		loggerFrom(ctx).Warn("authz: RequireSelfOrRole called with empty targetID",
			"subject", p.Subject)
	}

	// B4: Normalize both sides so canonical UUID case variants match. The
	// handler edge already produces canonical lowercase via ParseUUIDPathParam;
	// p.Subject may originate from an external IdP or pre-normalization data.
	// httputil.ParseCanonicalUUID is intentionally stricter than google/uuid.Parse:
	// brace-wrapped, urn:uuid:, and whitespace-padded forms are NOT recognized
	// here, so a subject in a non-canonical wire shape will not authorize a
	// canonical-shaped target via silent normalization. IdP adapters are
	// responsible for producing canonical subjects on intake.
	subject := p.Subject
	if canonical, ok := httputil.ParseCanonicalUUID(subject); ok {
		subject = canonical
	}
	target := targetID
	if canonical, ok := httputil.ParseCanonicalUUID(target); ok {
		target = canonical
	}

	if target != "" && subject == target {
		return nil
	}

	if principalHasAnyRole(p, bypassRoles) {
		return nil
	}

	return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, "access denied")
}

// principalHasAnyRole checks whether p holds at least one of the given roles.
// Returns false when roles is empty or p is nil.
func principalHasAnyRole(p *Principal, roles []string) bool {
	if p == nil || len(roles) == 0 {
		return false
	}
	return slices.ContainsFunc(roles, p.HasRole)
}

// RequireAnyRole checks that the authenticated subject holds at least one of
// the specified roles. Returns nil on success.
//
// Deprecated: prefer constructing auth.AnyRole(...) / auth.SelfOr(...) Policy
// and passing it via auth.Route.Policy. The internal Require* helpers below are
// retained only as the implementation backbone.
//
// Use this instead of RequireSelfOrRole for admin-only endpoints where there
// is no target resource owner to compare against.
//
// Calling with zero roles always returns ErrAuthForbidden (no role can match).
//
// Errors:
//   - ErrAuthUnauthorized: no Principal in context, or PrincipalUser with empty Subject
//   - ErrAuthForbidden: subject does not hold any of the required roles
func RequireAnyRole(ctx context.Context, roles ...string) error {
	p, ok := FromContext(ctx)
	if !ok {
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "authentication required")
	}
	// G1.B: Defense-in-depth. PrincipalUser must always carry a non-empty Subject;
	// an empty Subject indicates the primary authenticator allowed a malformed token
	// through. PrincipalAnonymous Subject is intentionally empty by design.
	// PrincipalService identity is expressed via CallerCellID, not Subject.
	if p.Kind == PrincipalUser && p.Subject == "" {
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "principal subject missing")
	}

	if principalHasAnyRole(p, roles) {
		return nil
	}

	return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, "access denied")
}

// TestContext creates a context carrying the given subject and roles for use
// in handler tests across cells/. Follows the net/http/httptest naming pattern.
//
// This helper is NOT deprecated; it is the recommended way to inject a
// Principal in handler tests.
//
// For service-principal tests, use TestServiceContext.
func TestContext(subject string, roles []string) context.Context {
	p := &Principal{
		Kind:       PrincipalUser,
		Subject:    subject,
		Roles:      append([]string(nil), roles...),
		AuthMethod: "test",
	}
	return WithPrincipal(context.Background(), p)
}

// RequireCallerCell returns a Policy that enforces the request is made by a
// service principal (PrincipalService) whose CallerCellID is in the allowlist.
//
// Use in auth.Route.Policy for internal endpoints that declare Clients in their
// ContractSpec; auth.Mount auto-applies this guard when spec.Clients is non-empty.
//
// Errors:
//   - ErrAuthUnauthorized: no Principal in context
//   - ErrAuthForbidden: Principal is not PrincipalService, or CallerCellID is
//     empty, or CallerCellID not in allowlist
func RequireCallerCell(allowlist ...string) Policy {
	set := make(map[string]bool, len(allowlist))
	sortedAllowlist := make([]string, len(allowlist))
	for i, c := range allowlist {
		lc := strings.ToLower(c)
		set[lc] = true
		sortedAllowlist[i] = lc
	}
	slices.Sort(sortedAllowlist)
	return func(r *http.Request) error {
		p, ok := FromContext(r.Context())
		if !ok {
			return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized, "authentication required")
		}
		if p.Kind != PrincipalService {
			return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden, "internal endpoint requires service token")
		}
		if p.CallerCellID == "" || !set[strings.ToLower(p.CallerCellID)] {
			return errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthForbidden,
				"caller_cell not in allowlist",
				errcode.WithInternal(fmt.Sprintf("caller_cell=%q allowlist=%v", p.CallerCellID, sortedAllowlist)))
		}
		return nil
	}
}
