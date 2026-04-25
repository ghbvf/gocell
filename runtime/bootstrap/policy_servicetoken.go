package bootstrap

import (
	"net/http"

	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/runtime/auth"
)

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
func PolicyServiceToken(store auth.NonceStore, ring *auth.HMACKeyRing) cell.Policy {
	if store == nil {
		panic("bootstrap: PolicyServiceToken store must not be nil")
	}
	if ring == nil {
		panic("bootstrap: PolicyServiceToken ring must not be nil")
	}
	return cell.Policy{
		Name: "service-token",
		Middleware: func(next http.Handler) http.Handler {
			return auth.ServiceTokenMiddleware(ring, auth.WithServiceTokenNonceStore(store))(next)
		},
	}
}
