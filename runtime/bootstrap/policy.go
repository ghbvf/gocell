package bootstrap

// policy.go — core Policy interface extensions and PolicyStack combinator.
//
// ref: go-chi/chi mux.go — Mux.Use semantics (middleware composition order)
// ref: go-kratos/kratos transport/http/server.go — ServerOption pattern
// ref: zeromicro/go-zero rest/server.go — WithJwt/WithJwtTransition RouteOption style

import (
	"fmt"
	"strings"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/go-chi/chi/v5"
)

// mountablePolicy is the runtime-internal extension of cell.Policy.
// Concrete implementations must satisfy both cell.Policy (for startup logging
// and archtest introspection) and this internal interface so bootstrap can
// Apply() their middleware to a chi.Mux at phase5.
//
// Apply is exported so that router.applyPolicyToMux (in a sibling package) can
// satisfy the interface via a local duck-typed check on the same exported method
// name — Go's structural typing requires exported method names to cross package
// boundaries.
type mountablePolicy interface {
	cell.Policy
	Apply(mux *chi.Mux)
}

// policyStack composes multiple policies into a single mountablePolicy.
// apply runs each constituent policy's middleware in slice order, so the
// first element in ps is the outermost middleware wrapper at invocation time.
//
// Note on ordering vs chi.Use semantics: chi.Use installs middleware such that
// the last-registered middleware is the outermost wrapper. policyStack reverses
// this: it calls apply() on each constituent policy in FIFO order, so each
// sub-policy's mux.Use call wraps *inside* the previous one. The net effect is
// that ps[0].apply() runs first, making ps[0] the outermost handler — the
// opposite of calling mux.Use(ps[0], ps[1], ...) directly. Document explicitly:
// "first in ps = outermost middleware".
type policyStack struct {
	policies []mountablePolicy
}

// Describe returns "stack[p0, p1, ...]" listing the constituent policy names.
// DX-02: replaces the static "stack" string so operators can tell which
// policies are composed without reading source code.
func (s *policyStack) Describe() string {
	if len(s.policies) == 0 {
		return "stack[]"
	}
	names := make([]string, len(s.policies))
	for i, p := range s.policies {
		names[i] = p.Describe()
	}
	return "stack[" + strings.Join(names, ", ") + "]"
}

func (s *policyStack) Apply(mux *chi.Mux) {
	// Apply in forward order: ps[0].Apply first means ps[0]'s Use is
	// registered before ps[1]'s, making ps[0] the outermost wrapper.
	for _, p := range s.policies {
		p.Apply(mux)
	}
}

// PolicyStack composes multiple policies into a single cell.Policy. When the
// resulting policy is applied to a mux, each constituent policy's middleware is
// installed in the order provided: ps[0] is the outermost middleware wrapper.
//
// CORR-05/SEC-14: every entry in ps must satisfy mountablePolicy. Passing a
// cell.Policy that does not implement mountablePolicy (e.g. a test double from
// outside runtime/bootstrap) panics at composition time with an actionable
// message. This is consistent with the nil-arg panics on other bootstrap
// constructors — fail at composition root, not at first HTTP request.
//
// For an empty ps, PolicyStack returns a no-op stack that passes all requests
// through. This is the "zero policies = allow all" fallback.
func PolicyStack(ps ...cell.Policy) cell.Policy {
	mountable := make([]mountablePolicy, 0, len(ps))
	for _, p := range ps {
		mp, ok := p.(mountablePolicy)
		if !ok {
			panic(fmt.Sprintf(
				"bootstrap: PolicyStack: policy %q (type %T) does not implement mountablePolicy; "+
					"only policies from runtime/bootstrap (PolicyJWT, PolicyServiceToken, PolicyMTLS, "+
					"PolicyVerboseToken, PolicyNone) may be composed in a PolicyStack",
				p.Describe(), p))
		}
		mountable = append(mountable, mp)
	}
	return &policyStack{policies: mountable}
}
