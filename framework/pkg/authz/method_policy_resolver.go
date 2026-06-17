package authz

// MethodPolicyResolver maps a transport's method/route key to the sealed
// authz.Permission that key's PDP gate requires. It is the transport-neutral
// seam that unifies HTTP route authorization and gRPC method authorization onto
// one contract-derived source (#2205): both transports resolve a permission the
// same way — through a MethodPolicyResolver backed by contract metadata — instead
// of HTTP hand-wiring auth.RequirePermission(authz.PermX()) while gRPC derives
// from endpoints.grpc.methods[].permission.
//
// # What is neutral, and what is not
//
// The KEY is transport-specific BY DESIGN, and that is not a leak in the
// abstraction:
//
//   - gRPC passes the full method name, e.g.
//     "/auth.session.verify.v1.SessionVerifyService/VerifyToken"
//     (one service contract carries many methods; the interceptor is centralized
//     and looks each up).
//   - HTTP passes the contract id, e.g. "http.config.get.v1"
//     (one HTTP contract is a single method+path; the per-route gate looks up its
//     own contract at Mount time).
//
// Forcing a single key space would require a synthetic "transport:id" encoding
// that NO consumer would ever query — a cost with no payer. The genuinely shared,
// load-bearing parts are the RETURN contract (a sealed Permission + a fail-closed
// ok bool) and the BACKING discipline (the value is contract-derived and resolved
// through the closed registry, never minted ad hoc). ok=false means "no
// contract-derived permission mapping for this key": the caller fails closed (the
// gate denies / refuses to mount an ungated standard route), it does NOT fall back
// to an implicit allow.
//
// # Implementations (≥2 genuine consumers — not a premature abstraction)
//
//   - gRPC: *runtime/grpc.ServiceRegistrar satisfies this interface directly; its
//     PermissionForMethod already had this exact signature (the per-method overlay
//     map built by recordMethodOverlays from endpoints.grpc.methods[].permission).
//   - HTTP: runtime/auth.NewStaticMethodPolicyResolver builds a cell-level resolver
//     from a contractID→action map that cellgen derives from each served HTTP
//     contract's endpoints.http.permission overlay; the generated handler's gate
//     (runtime/auth.RequirePermissionByName) resolves through it.
//
// # AI-robust grade
//
// The interface itself is a plain Go type. The seal that matters is the RETURN
// value: Permission is sealed (its sole minter is this package's registry), so a
// resolver cannot fabricate a permission outside the closed set — it can only hand
// back a pre-registered one or report ok=false. The contract→permission binding is
// golden-locked at codegen (Hard) and the wiring funnel is archtest-locked
// (Medium); see the runtime/auth + cellgen sources and HTTP-PERMISSION-GATE-WIRING-FUNNEL-01.
type MethodPolicyResolver interface {
	// PermissionForMethod returns the Permission required by methodKey's PDP gate
	// and ok=true when a contract-derived mapping exists; the zero Permission and
	// ok=false when none does (the caller fails closed). methodKey is the
	// transport-native identifier — gRPC full method name, HTTP contract id.
	PermissionForMethod(methodKey string) (Permission, bool)
}
