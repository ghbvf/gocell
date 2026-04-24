package bootstrap

// policy.go — core Policy interface extensions and PolicyStack combinator.
//
// ref: go-chi/chi mux.go — Mux.Use semantics (middleware composition order)
// ref: go-kratos/kratos transport/http/server.go — ServerOption pattern
// ref: zeromicro/go-zero rest/server.go — WithJwt/WithJwtTransition RouteOption style

import (
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/go-chi/chi/v5"
)

// mountablePolicy is the runtime-internal extension of cell.Policy.
// Concrete implementations must satisfy both cell.Policy (for startup logging
// and archtest introspection) and this internal interface so bootstrap can
// apply() their middleware to a chi.Mux at phase5.
//
// Cells only ever see the opaque cell.Policy interface; the apply method is
// entirely internal to runtime/bootstrap.
type mountablePolicy interface {
	cell.Policy
	apply(mux *chi.Mux)
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

func (s *policyStack) Describe() string { return "stack" }

func (s *policyStack) apply(mux *chi.Mux) {
	// Apply in forward order: ps[0].apply first means ps[0]'s Use is
	// registered before ps[1]'s, making ps[0] the outermost wrapper.
	for _, p := range s.policies {
		p.apply(mux)
	}
}

// PolicyStack composes multiple policies into a single cell.Policy. When the
// resulting policy is applied to a mux, each constituent policy's middleware is
// installed in the order provided: ps[0] is the outermost middleware wrapper.
//
// Non-mountablePolicy entries (cell.Policy implementations from outside
// runtime/bootstrap) are silently skipped with no middleware installed.
// In practice all concrete policies returned by this package implement
// mountablePolicy; external Policy values satisfy the Describe() contract only
// and carry no middleware to install.
func PolicyStack(ps ...cell.Policy) cell.Policy {
	mountable := make([]mountablePolicy, 0, len(ps))
	for _, p := range ps {
		if mp, ok := p.(mountablePolicy); ok {
			mountable = append(mountable, mp)
		}
	}
	return &policyStack{policies: mountable}
}
