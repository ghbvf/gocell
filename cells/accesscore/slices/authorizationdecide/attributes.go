package authorizationdecide

import (
	"strconv"
	"strings"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/abac"
	"github.com/ghbvf/gocell/runtime/auth"
)

// attributeResolver resolves a Condition's (Source, Key) to the attribute
// value(s) used by the evaluator. It is fail-closed by construction: every
// resolve path returns found=false when the attribute cannot be supplied from a
// trusted source, and the evaluator treats a not-found attribute as an
// unsatisfied condition (see evaluator.go matchCondition).
//
// PR-7 wires two real attribute sources:
//
//   - Subject: the authenticated principal (trusted JWT claims, FR-012). Device
//     posture attributes for a device principal (Kind==PrincipalDevice) live in
//     the same Claims map and are reached through the default claims branch;
//     the principal kind itself is exposed via the "kind" key.
//   - Environment: clock-derived time attributes (the injected clock, never
//     time.Now()).
//
// Resource attributes have NO data source in PR-7: PolicyRepository.GetAttributes
// is owned by PR-9 (#1347). Resource conditions therefore resolve found=false and
// fail closed until PR-9 wires the source — recorded as a deliberate carve-out
// in research.md §1.1 and on issue #1347.
type attributeResolver struct {
	principal *auth.Principal // may be nil → subject attributes fail-closed
	now       time.Time       // injected clock reading for environment attributes
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
		// Resource attribute sourcing is deferred to PR-9 (#1347 GetAttributes);
		// no source exists yet → fail-closed.
		return nil, false
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
