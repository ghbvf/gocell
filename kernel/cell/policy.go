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
	// Some policies (notably JWT) leave Middleware nil and rely on Bootstrap
	// to install a router-aware variant via Extension.
	Middleware func(http.Handler) http.Handler
	// Validate, when non-nil, is invoked by Bootstrap during phase4 (after
	// cell init, before listener bind). Policies that resolve dependencies
	// lazily — for example a JWT policy that needs an IntentTokenVerifier
	// from an authProvider cell — implement Validate so the warm-up runs at
	// startup and any failure surfaces as a Run() error instead of a
	// per-request 5xx after the server is already accepting traffic.
	// Validate must be idempotent; Bootstrap calls it exactly once per run.
	Validate func() error
	// Extension is an opaque payload that runtime/bootstrap type-asserts to
	// a known interface based on Name. The kernel/cell layer must stay
	// runtime-agnostic, so the verifier or other policy-typed state is
	// carried here rather than in a typed field. Name is the contract:
	//   - Name="jwt" → Extension is auth.IntentTokenVerifier (eager) or
	//     a verifier-getter closure (lazy, from PolicyJWTFromAssembly).
	// Other Name values currently do not use Extension.
	Extension any
}

// IsZero reports whether the Policy is the zero value (no name, no middleware,
// no validator, no extension). Bootstrap treats a zero Policy identically to
// PolicyNone.
func (p Policy) IsZero() bool {
	return p.Name == "" && p.Middleware == nil && p.Validate == nil && p.Extension == nil
}
