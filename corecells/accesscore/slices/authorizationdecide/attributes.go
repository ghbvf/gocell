package authorizationdecide

import (
	"strconv"
	"strings"
	"time"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/framework/pkg/httputil"
	runtimeauth "github.com/ghbvf/gocell/framework/runtime/auth"
)

// subjectSource is the private interface for subject attribute resolution.
// It abstracts over two implementations:
//   - principalSubjectSource: derives attributes from an authenticated *auth.Principal
//     (the existing Authorize path, trusted JWT claims).
//   - descriptorSubjectSource: derives attributes from an auth.SubjectDescriptor
//     (the new AuthorizeAs explicit-subject path, #1904).
//
// The interface is private (unexported) to prevent external forgery. All
// resolveSubject lookups route through this interface — the fail-closed
// semantics (nil subject → not-found) are preserved across both implementations.
type subjectSource interface {
	// sub returns the subject identifier and whether it is present.
	// Implementations that canonicalize UUID subjects must do so here
	// (principalSubjectSource mirrors the existing resolveSubject "sub" path,
	// including httputil.ParseCanonicalUUID canonicalization).
	sub() (string, bool)
	// kind returns the principal kind string (e.g. "user", "device") and found.
	kind() (string, bool)
	// roles returns the subject's role list (always found, possibly empty).
	// Returns nil when the source carries no roles (e.g. descriptorSubjectSource).
	roles() []string
	// tenant returns the tenant ID string and whether it is present.
	// Returns ("", false) for an empty tenant.
	tenant() (string, bool)
	// claim returns a supplementary claim value by key and whether it is found.
	// Returns ("", false) for unknown keys or when the source carries no claims.
	claim(key string) (string, bool)
}

// principalSubjectSource implements subjectSource by delegating to an
// *auth.Principal. It reproduces the existing resolveSubject behavior
// EXACTLY — bit-for-bit semantics preservation is required so that the
// Authorize path is unchanged after the subjectSource refactoring.
type principalSubjectSource struct {
	p *runtimeauth.Principal // may be nil → all methods return ("", false) or nil
}

// sub returns the subject's identifier. A nil principal or empty Subject → not-found.
// UUID subjects are canonicalized via httputil.ParseCanonicalUUID (mirroring the
// RequirePermissionForResource canonicalization so subject.sub == resource.id is
// robust to UUID case/format differences). Non-UUID subjects pass through unchanged.
func (s principalSubjectSource) sub() (string, bool) {
	if s.p == nil || s.p.Subject == "" {
		return "", false
	}
	if c, ok := httputil.ParseCanonicalUUID(s.p.Subject); ok {
		return c, true
	}
	return s.p.Subject, true
}

// kind returns the principal kind string. Always present for a non-nil principal.
func (s principalSubjectSource) kind() (string, bool) {
	if s.p == nil {
		return "", false
	}
	return s.p.Kind.String(), true
}

// roles returns a copy of the principal's Roles slice. Always found (possibly empty)
// for a non-nil principal, to preserve the existing resolveSubject semantics where
// "roles" is always present (empty set, not not-found, for a role-less principal).
func (s principalSubjectSource) roles() []string {
	if s.p == nil {
		return nil
	}
	return append([]string(nil), s.p.Roles...)
}

// tenant returns the principal's TenantID and whether it is non-empty.
func (s principalSubjectSource) tenant() (string, bool) {
	if s.p == nil || s.p.TenantID == "" {
		return "", false
	}
	return s.p.TenantID, true
}

// claim returns a JWT claim value by key from the principal's Claims map.
func (s principalSubjectSource) claim(key string) (string, bool) {
	if s.p == nil || s.p.Claims == nil {
		return "", false
	}
	v, ok := s.p.Claims[key]
	return v, ok
}

// descriptorSubjectSource implements subjectSource by delegating to an
// auth.SubjectDescriptor. It carries no roles and no claims — a device
// descriptor is a minimal identity value (kind + sub + tenant only).
// Used by AuthorizeAs (the explicit-subject authorization path, #1904).
type descriptorSubjectSource struct {
	d runtimeauth.SubjectDescriptor
}

// sub returns the descriptor's Sub(). Empty Sub → not-found.
func (s descriptorSubjectSource) sub() (string, bool) {
	v := s.d.Sub()
	if v == "" {
		return "", false
	}
	return v, true
}

// kind returns the descriptor's Kind(). Empty Kind (zero value) → not-found.
func (s descriptorSubjectSource) kind() (string, bool) {
	v := s.d.Kind()
	if v == "" {
		return "", false
	}
	return v, true
}

// roles returns nil: a SubjectDescriptor carries no role information.
// The nil return means "roles" is absent (not an empty-set present), so
// a policy that requires subject.roles ∈ {admin} will fail-closed for a
// descriptor subject — device enrollment rules must not rely on roles.
func (s descriptorSubjectSource) roles() []string { return nil }

// tenant returns the descriptor's Tenant() string. Empty → not-found.
func (s descriptorSubjectSource) tenant() (string, bool) {
	v := s.d.Tenant()
	if v == "" {
		return "", false
	}
	return v, true
}

// claim returns ("", false) for all keys: a SubjectDescriptor carries no
// supplementary claim attributes (it is a minimal sealed identity value).
func (s descriptorSubjectSource) claim(_ string) (string, bool) { return "", false }

// attributeResolver resolves a Condition's (Source, Key) to the attribute
// value(s) used by the evaluator. It is fail-closed by construction: every
// resolve path returns found=false when the attribute cannot be supplied from a
// trusted source, and the evaluator treats a not-found attribute as an
// unsatisfied condition (see evaluator.go matchCondition).
//
// Three real attribute sources are wired (PR-7 + PR-9 #1347):
//
//   - Subject: the subject source (trusted JWT claims via principalSubjectSource,
//     or explicit SubjectDescriptor via descriptorSubjectSource). Device posture
//     attributes for a device principal (Kind==PrincipalDevice) live in the
//     Claims map and are reached through the default claims branch; the principal
//     kind is exposed via the "kind" key.
//   - Environment: clock-derived time attributes (the injected clock, never
//     time.Now()).
//   - Resource: attributes fetched from the injected ResourceAttributeProvider
//     (PIP, PR-9 #1347), scoped to the request tenant. The fetch happens inside
//     the same scopedtx.Do block as policy loading so both reads share a single
//     tenant binding (RESOURCE-ATTR-TENANT-SHARING-01). An absent key resolves
//     found=false — still fail-closed.
//
// resourceID is the identity key for the resource being accessed (#1977 Batch B):
// it is the canonicalized value of the resource-id path parameter forwarded by
// RequirePermissionForResource, distinct from PIP-provided attributes. Exposed
// as resource.id (via resolveResource "id" fast-path), it enables the identity-
// ownership baseline rule (subject.sub == resource.id). An empty resourceID means
// found=false (resource.id not-found → ownership rule can't fire, fail-closed).
// All other resource keys continue to resolve from the resourceAttrs PIP map.
type attributeResolver struct {
	subject       subjectSource       // subject attribute source (principal or descriptor)
	now           time.Time           // injected clock reading for environment attributes
	resourceAttrs map[string][]string // pre-fetched resource attrs for this request
	// resourceID is the resource identity passed to Authorize (the canonicalized
	// path param from RequirePermissionForResource). Exposed as resource.id.
	// Distinct from PIP-provided resourceAttrs — no DB lookup required.
	resourceID string
}

// resolve returns the value(s) for the attribute identified by (source, key) and
// whether the attribute was found. A false found means the attribute is not
// available from a trusted source; callers MUST treat that as an unsatisfied
// condition (fail-closed). Discarding the found result is a fail-open bug,
// statically guarded by AUTHZ-EVAL-ATTR-NOTFOUND-GUARD-01.
func (r attributeResolver) resolve(source abac.AttributeSource, key string) (vals []string, found bool) {
	switch source {
	case abac.SourceSubject:
		return r.resolveSubject(key)
	case abac.SourceEnvironment:
		return r.resolveEnvironment(key)
	case abac.SourceResource:
		return r.resolveResource(key)
	default:
		return nil, false
	}
}

// resolveSubject reads subject attributes from the subjectSource.
// Well-known keys are dispatched to dedicated methods; any other key falls
// through to the claims map. A nil subjectSource supplies no attributes.
//
// The "sub"/"subject" case canonicalizes the principal's Subject via
// httputil.ParseCanonicalUUID before returning (via principalSubjectSource.sub),
// mirroring the resource.id gate canonicalization in RequirePermissionForResource
// (so `subject.sub == resource.id` is robust to UUID case/format differences,
// matching the pre-#1977 isSelfAccess behavior). Non-UUID subjects (e.g. service
// accounts with a plain-string Subject) pass through unchanged.
func (r attributeResolver) resolveSubject(key string) (vals []string, found bool) {
	if r.subject == nil {
		return nil, false
	}
	switch key {
	case "sub", "subject":
		v, ok := r.subject.sub()
		if !ok {
			return nil, false
		}
		return []string{v}, true
	case "kind":
		// Always present for a non-nil subject; exposes user/device/service
		// so policies can branch on subject kind (device-posture matrix).
		v, ok := r.subject.kind()
		if !ok {
			return nil, false
		}
		return []string{v}, true
	case "role", "roles":
		// Multi-valued. For a principal source the roles attribute always exists
		// (possibly empty); for a descriptor source it is nil → treat as found
		// but empty set (consistent with the existing principal behavior). The
		// evaluator handles an empty []string correctly for membership operators.
		return r.subject.roles(), true
	case "tenant", "tenant_id":
		v, ok := r.subject.tenant()
		if !ok {
			return nil, false
		}
		return []string{v}, true
	default:
		v, ok := r.subject.claim(key)
		if !ok {
			return nil, false
		}
		return []string{v}, true
	}
}

// resolveEnvironment reads time-derived environment attributes from the injected
// clock reading. The supported key set is intentionally small and bounded
// (test-driven); an unknown key is fail-closed.
func (r attributeResolver) resolveEnvironment(key string) (vals []string, found bool) {
	switch key {
	case "hour":
		return []string{strconv.Itoa(r.now.Hour())}, true
	case "day_of_week":
		return []string{strings.ToLower(r.now.Weekday().String())}, true
	default:
		return nil, false
	}
}

// resolveResource reads resource attributes. The "id" key is a fast-path that
// returns the resourceID identity field (set from RequirePermissionForResource's
// canonicalized path param, #1977 Batch B) — no DB lookup. An empty resourceID
// means found=false so the ownership condition is unsatisfied (fail-closed).
// All other keys resolve from the pre-fetched resourceAttrs PIP map
// (RESOURCE-ATTR-TENANT-SHARING-01). An absent key → found=false (fail-closed).
func (r attributeResolver) resolveResource(key string) (vals []string, found bool) {
	if key == "id" {
		if r.resourceID == "" {
			return nil, false
		}
		return []string{r.resourceID}, true
	}
	vals, ok := r.resourceAttrs[key]
	return vals, ok
}
