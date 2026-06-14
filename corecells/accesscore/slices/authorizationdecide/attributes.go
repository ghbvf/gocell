package authorizationdecide

import (
	"strconv"
	"strings"
	"time"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/framework/pkg/httputil"
	"github.com/ghbvf/gocell/framework/runtime/auth"
)

// attributeResolver resolves a Condition's (Source, Key) to the attribute
// value(s) used by the evaluator. It is fail-closed by construction: every
// resolve path returns found=false when the attribute cannot be supplied from a
// trusted source, and the evaluator treats a not-found attribute as an
// unsatisfied condition (see evaluator.go matchCondition).
//
// Three real attribute sources are wired (PR-7 + PR-9 #1347):
//
//   - Subject: the authenticated principal (trusted JWT claims, FR-012). Device
//     posture attributes for a device principal (Kind==PrincipalDevice) live in
//     the same Claims map and are reached through the default claims branch;
//     the principal kind itself is exposed via the "kind" key.
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
	principal     *auth.Principal     // may be nil → subject attributes fail-closed
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

// resolveSubject reads subject attributes from the authenticated principal.
// Well-known keys are derived from typed principal fields; any other key falls
// through to the JWT claims snapshot. A nil principal supplies no attributes.
//
// The "sub"/"subject" case canonicalizes the principal's Subject via
// httputil.ParseCanonicalUUID before returning, mirroring the resource.id gate
// canonicalization in RequirePermissionForResource (so `subject.sub ==
// resource.id` is robust to UUID case/format differences, matching the
// pre-#1977 isSelfAccess behavior). Non-UUID subjects (e.g. service accounts
// with a plain-string Subject) pass through unchanged.
func (r attributeResolver) resolveSubject(key string) (vals []string, found bool) {
	if r.principal == nil {
		return nil, false
	}
	switch key {
	case "sub", "subject":
		if r.principal.Subject == "" {
			return nil, false
		}
		if c, ok := httputil.ParseCanonicalUUID(r.principal.Subject); ok {
			return []string{c}, true
		}
		return []string{r.principal.Subject}, true
	case "kind":
		// Always present for a non-nil principal; exposes user/device/service
		// so policies can branch on subject kind (device-posture matrix).
		return []string{r.principal.Kind.String()}, true
	case "role", "roles":
		// Multi-valued. The roles attribute always exists for a principal
		// (possibly empty); membership operators evaluate over the set.
		return append([]string(nil), r.principal.Roles...), true
	case "tenant", "tenant_id":
		if r.principal.TenantID == "" {
			return nil, false
		}
		return []string{r.principal.TenantID}, true
	default:
		if v, ok := r.principal.Claims[key]; ok {
			return []string{v}, true
		}
		return nil, false
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
