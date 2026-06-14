package idempotency

// IdempotencyKey is the sealed (namespace, key) pair that scopes one idempotency
// record. Its fields are unexported, so a populated IdempotencyKey{...} literal
// cannot be constructed outside this package — the only producers are the
// sanctioned constructors DeriveKey (HTTP records) and DeriveCommandKey (commands).
// Stores read it through Namespace()/Key().
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
//   - require-isolation-tuple (each sanctioned constructor's body must FLOW all
//     its dimensions into the key) — Medium, genuine Go ceiling: Go cannot express
//     "a body consumes all its inputs", and the same-type string params could be
//     transposed at the (single, reviewed) callsite. The β archtest's AST taint
//     walk (with a flat-composition guard that rejects value laundering) is the
//     Medium backstop. Won't-do ceiling tracked at gh #1650 (same family as
//     #851/#893/#1282/#1552).
//
// # Two sanctioned constructors (HTTP record + cross-cell command, #1669)
//
// DeriveKey mints the HTTP record key. DeriveCommandKey (#1669) mints the command
// dedup key from (tenant, subject, command_id) — the bridge primitive the
// cross-cell same-slot half of #1610 consumes (routing one logical command to a
// single dedup slot across cells). Both produce this SAME sealed IdempotencyKey
// and flow through the SAME Store.Claim sink — the sealed type + funnel are the
// durable infra and need no change. The archtest sole-producer allowlist + the
// per-constructor signature/taint gate are single-sourced from one table
// (idempotencyKeyConstructors), so both constructors are locked identically and a
// third producer cannot be added to one gate while skipping the other.
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

// Flat flattens the sealed (ns, key) pair into the single string key that
// kernel/idempotency.Claimer.Claim(ctx, key string, …) consumes. It is the SOLE
// flattening exit for an IdempotencyKey into the claimer string-key domain.
//
// Encoding: ns + "\x00" + key. The NUL (\x00) separator is consistent with the
// NUL separators already embedded inside ns/key by DeriveKey / DeriveCommandKey,
// and tenant ids / command ids are opaque tokens free of NUL, so the boundary is
// unambiguous (subject="alic",rest="e:x" never collides with subject="alice",
// rest="x" — a colon separator would).
//
// Node-agnostic (assembly-scope) invariant — inherited from the type godoc: the
// flat key carries NO pod/listener/cell dimension; it is derived ONLY from the
// (ns,key) pair, which is itself derived ONLY from request + principal data. The
// claimer dedup domain is therefore assembly-wide, matching the replay store.
func (k IdempotencyKey) Flat() string { return k.ns + "\x00" + k.key }

// noTenantSentinel is the namespace substituted when the authenticated principal
// carries no tenant (e.g. a service principal: callerCellID is not a tenant).
const noTenantSentinel = "_notenant"

// DeriveKey is the HTTP-record constructor of an IdempotencyKey (DeriveCommandKey
// is the sibling command constructor; the two are the sanctioned producers). It
// encodes the isolation tuple (tenantID, subject, method, path, idemKey) into the
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

// DeriveCommandKey is the SECOND sanctioned constructor of an IdempotencyKey
// (sibling of DeriveKey, pre-blessed in this file's type godoc). It encodes the
// cross-cell command isolation tuple (tenantID, subject, commandID) into the
// (namespace, key) pair stores expect — the #1610 cross-cell same-slot mapping
// primitive (ADR docs/architecture/202606040550-1044-adr-command-bus-dispatch-funnel.md
// §5 ⑤):
//
//	ns  = tenantID, or noTenantSentinel when empty.
//	key = subject + "\x00" + commandID
//
// commandID is the per-instance idempotency identity of a dispatched command — an
// OPAQUE string that uniquely names one logical command instance. It is NOT the
// contract-level command identifier (the kind:command contract id /
// runtime/command.CommandID used to route a command to its handler): two distinct
// invocations of the same contract command carry DIFFERENT commandID values and
// MUST occupy different dedup slots. Sourcing a stable per-instance commandID is
// the caller's job (the deferred relay-side Claimer wrap, ⑤ PR-B). Because
// runtime/command.CommandID is the contract-level routing id (a different
// concept), callers pass the per-instance identity as a plain string here, e.g.
// DeriveCommandKey(string(tenantID), string(subject), instanceID).
//
// Caller obligations (same store-side contract as DeriveKey, deliberately NOT
// folded into this store-agnostic constructor): commandID is opaque and may be
// untrusted; the Redis-backed store rejects a key whose body contains the
// Redis-Cluster hash-tag braces "{"/"}" or is empty (returns KindInternal on
// Claim → permanent error → MarkDead). The caller MUST therefore validate
// commandID is brace-free and non-empty before deriving (DeriveKey's HTTP path
// does this in middleware on the Idempotency-Key header). When subject is empty
// (a service principal with no subject), the slot is distinguished within its
// tenant namespace by commandID alone, so the caller MUST keep commandID unique
// in that scope.
//
// Why no method/path (unlike DeriveKey): DeriveKey scopes an HTTP record per
// endpoint so the same Idempotency-Key header on POST /orders and POST /payments
// stays independent. A command is ALREADY one logical operation named by
// commandID; routing it to ONE dedup slot across cells is the whole point of
// #1610, so folding method/path back in would re-partition the slot per
// listener/endpoint and defeat cross-cell same-slot. commandID IS the per-instance
// identity that method+path+idemKey jointly play for HTTP. The NUL (\x00)
// separator is unambiguous: command ids are opaque tokens free of NUL, so
// subject="alic",commandID="e:x" never collides with subject="alice",commandID="x"
// (a colon separator would) — same rationale as DeriveKey.
//
// Assembly-scope (node-agnostic) invariant — identical to DeriveKey: the result is
// derived ONLY from its three parameters; there is no pod/listener/cell input and
// no call reading node-local state. Do NOT add a node/listener/cell parameter (a
// 4th param is the node-id injection vector the β archtest signature freeze
// rejects). All three params MUST flow into the result; dropping one collapses
// cross-tenant / cross-subject / cross-command isolation (the Medium taint-walk
// backstop, gh #1650).
func DeriveCommandKey(tenantID, subject, commandID string) IdempotencyKey {
	ns := tenantID
	if ns == "" {
		ns = noTenantSentinel
	}
	return IdempotencyKey{
		ns:  ns,
		key: subject + "\x00" + commandID,
	}
}
