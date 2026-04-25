package cell

import "net/http"

// Policy is a value type that pairs a human-readable name with an HTTP
// middleware function. Bootstrap applies a Policy to the chi.Mux for a
// listener during phase5: if Middleware is non-nil it is installed via
// mux.Use, otherwise the policy is a no-op (PolicyNone semantics).
//
// Concrete Policy values are constructed by factory functions in
// runtime/bootstrap (PolicyNone, PolicyJWT, PolicyServiceToken, PolicyMTLS,
// PolicyVerboseToken, PolicyStack). Cells receive them as opaque values and
// pass them to bootstrap.WithListener — they must not inspect the internals.
type Policy struct {
	// Name is a human-readable policy identifier for startup logging and
	// archtest introspection. Does not contain secrets.
	Name string
	// Middleware is the HTTP middleware to install on the listener's mux.
	// A nil Middleware is valid — it means "no-op" (PolicyNone semantics).
	Middleware func(http.Handler) http.Handler
}

// IsZero reports whether the Policy is the zero value (no name, no middleware).
// Bootstrap treats a zero Policy identically to PolicyNone.
func (p Policy) IsZero() bool {
	return p.Name == "" && p.Middleware == nil
}
