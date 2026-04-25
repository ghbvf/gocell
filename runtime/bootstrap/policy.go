package bootstrap

// policy.go — PolicyStack combinator.
//
// ref: go-chi/chi mux.go — Mux.Use semantics (middleware composition order)
// ref: go-kratos/kratos transport/http/server.go — ServerOption pattern

import (
	"net/http"
	"strings"

	"github.com/ghbvf/gocell/kernel/cell"
)

// PolicyStack composes multiple policies into a single cell.Policy. When the
// resulting policy is applied to a mux, each constituent policy's middleware is
// installed in the order provided: ps[0] is the outermost middleware wrapper.
//
// For an empty ps, PolicyStack returns a no-op policy (PolicyNone semantics).
// Nil-Middleware policies in ps are silently skipped.
//
// JWT policies are intentionally rejected: PolicyJWT / PolicyJWTFromAssembly
// carry their verifier in Extension and rely on Bootstrap to install the
// router-aware AuthMiddleware. Stacking them would preserve only the display
// name and silently drop the verifier.
func PolicyStack(ps ...cell.Policy) cell.Policy {
	var middlewares []func(http.Handler) http.Handler
	names := make([]string, 0, len(ps))
	for _, p := range ps {
		if p.Name == "jwt" {
			panic("bootstrap: PolicyStack does not support PolicyJWT or PolicyJWTFromAssembly; pass JWT directly as the listener default policy")
		}
		names = append(names, p.Name)
		if p.Middleware != nil {
			middlewares = append(middlewares, p.Middleware)
		}
	}

	name := "stack[" + strings.Join(names, ", ") + "]"
	if len(middlewares) == 0 {
		return cell.Policy{Name: name}
	}

	return cell.Policy{
		Name: name,
		Middleware: func(next http.Handler) http.Handler {
			// Compose in reverse so that ps[0] is the outermost wrapper.
			h := next
			for i := len(middlewares) - 1; i >= 0; i-- {
				h = middlewares[i](h)
			}
			return h
		},
	}
}
