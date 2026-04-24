package bootstrap

import (
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/go-chi/chi/v5"
)

// policyServiceToken applies HMAC-SHA256 service token middleware.
type policyServiceToken struct {
	store auth.NonceStore
	ring  *auth.HMACKeyRing
}

func (p *policyServiceToken) Describe() string { return "service-token" }

func (p *policyServiceToken) apply(mux *chi.Mux) {
	mux.Use(auth.ServiceTokenMiddleware(p.ring, auth.WithServiceTokenNonceStore(p.store)))
}

// PolicyServiceToken returns a cell.Policy that installs the HMAC-SHA256
// service token authentication middleware.
//
// Fail-fast rules (programmer-error panics at construction time):
//   - store nil → panic "bootstrap: PolicyServiceToken store must not be nil"
//   - ring nil → panic "bootstrap: PolicyServiceToken ring must not be nil"
//
// Both dependencies are required for a properly guarded internal listener.
// Noop/test doubles may be passed for non-production deployments.
//
// ref: PR-A25 ErrControlplaneNonceStoreMissing style — fail-fast at startup for nil deps.
func PolicyServiceToken(store auth.NonceStore, ring *auth.HMACKeyRing) *policyServiceToken {
	if store == nil {
		panic("bootstrap: PolicyServiceToken store must not be nil")
	}
	if ring == nil {
		panic("bootstrap: PolicyServiceToken ring must not be nil")
	}
	return &policyServiceToken{store: store, ring: ring}
}
