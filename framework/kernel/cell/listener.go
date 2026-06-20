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
	// WebhookListener is the dedicated listener for inbound webhook receive
	// endpoints. Inbound webhooks authenticate at the application layer via HMAC
	// signature verification inside the receiver (kernel/webhook), NOT via a
	// transport auth chain — so its WithListener auth chain is auth.AuthNone{}.
	// It is kept off PrimaryListener precisely because PrimaryListener typically
	// carries a JWT chain that would 401 an HMAC-signed (non-JWT) webhook before
	// it ever reaches the verifier. See .claude/rules/gocell/runtime-api.md
	// §"单 listener 单 auth scheme".
	WebhookListener = ListenerRef{"webhook"}
	// AdminListener is the dedicated listener for operator control-plane routes
	// (/admin/v1/*) — operator→system actions (projection rebuild, future saga
	// control / reconcile triggers) triggered by an administrator or deployment
	// pipeline, NOT cell→cell business calls. It carries an operator-credential
	// auth chain (auth.AuthOperator: HTTP Basic Auth over env credentials with
	// per-IP rate limiting), and is bound to a loopback address — loopback
	// network isolation plus operator credentials form a defense-in-depth pair.
	// Unlike InternalListener (/internal/v1/*, cell→cell, service-token +
	// caller-cell allowlist), the admin plane has no caller-cell notion; the
	// operator is authenticated directly. Mirrors the EventStoreDB projection
	// admin API model (a network-isolated admin port + operator credentials).
	AdminListener = ListenerRef{"admin"}
	// DeviceMTLSListener is the dedicated listener for EST certificate-renewal
	// endpoints that authenticate the enrolling device by its existing client
	// certificate (mutual TLS). It carries kernel/auth AuthMTLS{} as its sole
	// auth scheme.
	//
	// It must be separate from PrimaryListener because PrimaryListener's JWT
	// chain would 401 a non-JWT mTLS request before it ever reaches the EST
	// handler — a single listener can carry only one auth scheme (see
	// .claude/rules/gocell/runtime-api.md §"单 listener 单 auth scheme").
	// EST certificate renewal authenticates the device by the client certificate
	// it holds, not by a JWT issued after enrolment, so a dedicated mTLS
	// listener is the correct separation.
	DeviceMTLSListener = ListenerRef{"device-mtls"}
)
