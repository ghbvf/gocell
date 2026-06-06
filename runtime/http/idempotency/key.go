package idempotency

// IdempotencyKey is the sealed (namespace, key) pair that scopes one HTTP
// idempotency record. Its fields are unexported, so a populated
// IdempotencyKey{...} literal cannot be constructed outside this package — the
// ONLY producer is DeriveKey. Stores read it through Namespace()/Key().
//
// # Why sealed (the node-agnostic invariant, #1449/#1610)
//
// The framework HTTP idempotency replay store is assembly-wide: every pod in an
// assembly sharing one Redis deduplicates the same logical request. That rests
// on the (ns,key) carrying NO per-pod / per-listener / per-cell dimension — it
// is derived ONLY from request + principal data. Sealing the type makes that
// structural, on two axes (AI-robust grading, frozen by archtest
// HTTP-IDEMPOTENCY-KEY-NODE-AGNOSTIC-01; governance真值源 = ADR
// docs/architecture/202606051000-1449-adr-http-idempotency-assembly-scope-namespace.md):
//
//   - node-agnostic / ban-external-source — Hard, both directions:
//     upstream-external Hard (unexported fields ⇒ no other package can mint a key
//     and splice in node-local state — "sealed construction") + downstream Hard
//     (Store.Claim takes IdempotencyKey, so a raw (ns,key) string pair is
//     inexpressible at the store boundary). NOTE: upstream is Hard only against
//     OTHER packages; an in-package populated literal or a second in-package
//     producer is NOT a compile error — the archtest sole-producer freeze is the
//     Medium backstop there (same split as metrics.CellLabel / holder-seal #893).
//   - require-isolation-tuple (DeriveKey's body must FLOW all five dimensions
//     into the key) — Medium, genuine Go ceiling: Go cannot express "a body
//     consumes all its inputs", and the five same-type string params could be
//     transposed at the (single, reviewed) callsite. The β archtest's AST taint
//     walk is the Medium backstop. Won't-do ceiling tracked at gh #1650 (same
//     family as #851/#893/#1282/#1552).
//
// # Forward compatibility (cross-cell, #1610 / blocked-by #1044)
//
// Routing one logical command to a single dedup slot ACROSS cells (the cross-cell
// half of #1610) needs an Idempotency-Key ↔ command_id bridge that does not yet
// exist (deferred to #1044's Command Bus). When it lands, a sibling constructor
// (e.g. DeriveCommandKey) will produce this SAME sealed IdempotencyKey from
// (tenant, subject, command_id) — the sealed type + Store.Claim funnel are the
// durable infra and need no change; only the archtest sole-producer allowlist
// gains the new constructor.
type IdempotencyKey struct {
	ns  string
	key string
}

// Namespace returns the idempotency namespace that scopes keys to a tenant (or
// noTenantSentinel when the principal carries no tenant). Redis-backed stores
// use it as the tenant prefix segment outside the hash-tag; the per-request
// Key() value is the hash-tag payload that colocates lease/response/fingerprint
// keys on one cluster slot.
func (k IdempotencyKey) Namespace() string { return k.ns }

// Key returns the per-request key within the namespace.
func (k IdempotencyKey) Key() string { return k.key }

// noTenantSentinel is the namespace substituted when the authenticated principal
// carries no tenant (e.g. a service principal: callerCellID is not a tenant).
const noTenantSentinel = "_notenant"

// DeriveKey is the SOLE constructor of an IdempotencyKey. It encodes the
// isolation tuple (tenantID, subject, method, path, idemKey) into the
// (namespace, key) pair stores expect:
//
//	ns  = tenantID, or noTenantSentinel when empty.
//	key = subject + "\x00" + method + "\x00" + path + "\x00" + idemKey
//
// Including method+path means the same Idempotency-Key header value is
// independent per endpoint — POST /orders and POST /payments with the same header
// are separate records (aligned with Stripe's idempotency design and the IETF
// idempotency-key draft §3). The NUL (\x00) separator cannot appear in HTTP
// header values (RFC 7230 §3.2.6 limits field-value to VCHAR/obs-text, excluding
// NUL), so subject="alic",rest="e:x" never collides with subject="alice",rest="x"
// — a colon separator would.
//
// Assembly-scope (node-agnostic) invariant: the result is derived ONLY from its
// parameters — there is no pod/listener/cell input and no call that could read
// node-local state. That is what makes the replay domain assembly-wide. Do NOT
// add a node/listener/cell parameter (a 6th param is the node-id injection vector
// the β archtest signature freeze rejects). All five params MUST flow into the
// result; dropping one silently collapses cross-tenant / cross-user /
// cross-endpoint isolation (the Medium taint-walk backstop, gh #1650).
//
// DeriveKey is error-free: brace/empty rejection for Redis-cluster hash-tags is a
// store-specific concern handled by the Redis adapter on Claim, NOT folded here
// (MemStore has no such constraint — over-coupling the store-agnostic key to
// Redis would be wrong).
func DeriveKey(tenantID, subject, method, path, idemKey string) IdempotencyKey {
	ns := tenantID
	if ns == "" {
		ns = noTenantSentinel
	}
	return IdempotencyKey{
		ns:  ns,
		key: subject + "\x00" + method + "\x00" + path + "\x00" + idemKey,
	}
}
