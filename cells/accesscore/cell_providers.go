// cell_providers.go hosts AccessCore's "exposed service" provider methods
// — accessors that other layers (runtime/auth middleware) consume to wire
// cross-cutting concerns. Routing, event subscription, health probe, and
// lifecycle wiring now live in cell_init.go (Batch 3 Registry migration).
// Constructor + options live in cell.go; Init() + slice construction in cell_init.go.
package accesscore

import (
	"github.com/ghbvf/gocell/runtime/auth"
)

// TokenVerifier returns the session-validate service. It satisfies
// auth.IntentTokenVerifier so it can be plugged into AuthMiddleware without
// a runtime type assertion.
func (c *AccessCore) TokenVerifier() auth.IntentTokenVerifier {
	if c.validateSvc == nil {
		return nil
	}
	return c.validateSvc
}

// Authorizer returns the authorization-decide service (implements auth.Authorizer).
func (c *AccessCore) Authorizer() auth.Authorizer {
	return c.authzSvc
}
