package authorizationdecide

import (
	"strconv"
	"strings"
	"time"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/runtime/auth"
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
type attributeResolver struct {
	principal     *auth.Principal     // may be nil → subject attributes fail-closed
	now           time.Time           // injected clock reading for environment attributes
	resourceAttrs map[string][]string // pre-fetched resource attrs for this request
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
func (r attributeResolver) resolveSubject(key string) (vals []string, found bool) {
	if r.principal == nil {
		return nil, false
	}
	switch key {
	case "sub", "subject":
		if r.principal.Subject == "" {
			return nil, false
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

// resolveResource reads resource attributes from the pre-fetched resourceAttrs
// map (populated by ResourceAttributeProvider.GetAttributes inside the same
// tenant-scoped tx block as policy loading — RESOURCE-ATTR-TENANT-SHARING-01).
// An absent key resolves found=false → fail-closed (condition unsatisfied).
func (r attributeResolver) resolveResource(key string) (vals []string, found bool) {
	vals, ok := r.resourceAttrs[key]
	return vals, ok
}
