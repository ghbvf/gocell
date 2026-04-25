// Package cell provides the fundamental kernel types for the GoCell framework.
// This file defines the [ListenerRef] type — a type-safe, compile-time-validated
// reference to a physical HTTP listener.
//
// Design note: the [name] field is intentionally unexported so that no external
// package can construct an arbitrary ListenerRef by literal. Cells express their
// listener intent only via the exported package-level variables
// ([PrimaryListener], [InternalListener], [HealthListener]), eliminating the
// entire class of listener-name typos that a bare string parameter would allow.
//
// Listener topology ownership: the ListenerRef set is intentionally kernel-owned
// and closed. A new listener class (e.g. a future AdminListener) must be added
// here in kernel/cell rather than manufactured by an individual cell. This is
// deliberate: listener topology is a deployment concern (which physical ports
// exist, what policies apply, how probes are routed) that belongs to the
// assembly/bootstrap level, not to individual cells. Cells declare *which*
// listener their routes target, but do not *define* listeners; that separation
// keeps cells portable across assemblies that share the same listener vocabulary.
package cell

// ListenerRef is a type-safe reference to a physical HTTP listener.
// The name field is unexported so external packages cannot construct
// illegal references; this is the compile-time listener-reference
// validation from PR-A14b.
type ListenerRef struct{ name string }

// String returns the listener's canonical name.
func (r ListenerRef) String() string { return r.name }

// IsZero reports whether the ref is the zero value (no listener assigned).
func (r ListenerRef) IsZero() bool { return r.name == "" }

// Package-level listener references. Cells must use these variables to express
// their listener intent; bare string construction is intentionally prevented by
// the unexported name field.
var (
	// PrimaryListener is the public-facing listener for /api/v1/* business routes.
	PrimaryListener = ListenerRef{"primary"}
	// InternalListener is the control-plane listener for /internal/v1/* routes.
	InternalListener = ListenerRef{"internal"}
	// HealthListener is the dedicated listener for /healthz, /readyz, /metrics.
	HealthListener = ListenerRef{"health"}
)
