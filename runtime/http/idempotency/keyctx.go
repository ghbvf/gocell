package idempotency

import "context"

// requestKeyCtxKey is the unexported context-key type carrying the request's
// validated Idempotency-Key value to downstream handlers. A dedicated struct
// type (not a string) prevents collisions with other packages' context keys.
type requestKeyCtxKey struct{}

// WithKey returns a child context carrying the request's Idempotency-Key value k.
//
// The HTTP idempotency middleware calls WithKey after validating the
// Idempotency-Key header so a downstream handler can map the client-chosen
// idempotency token to a per-instance command_id — the HTTP-side half of the
// #1610 Idempotency-Key ↔ command_id bridge. The sanctioned downstream consumer
// is runtime/command.EmitAsyncFromIdempotencyKey, which reads it via
// KeyFromContext.
//
// On the middleware path k is already edge-validated (non-empty, ≤256 bytes,
// brace-free, printable). WithKey is exported so producers can also inject a key
// in tests or custom flows; a key injected directly is NOT guaranteed validated,
// so the command bridge re-checks (non-empty, brace-free) before deriving the
// dedup key.
func WithKey(ctx context.Context, k string) context.Context {
	return context.WithValue(ctx, requestKeyCtxKey{}, k)
}

// KeyFromContext returns the Idempotency-Key carried in ctx by the HTTP
// idempotency middleware (or by WithKey), if present. ok=false when the request
// carried no Idempotency-Key, the route is idempotency-exempt, or the method is
// non-idempotent — the middleware injects only on intercepted, validated
// requests.
func KeyFromContext(ctx context.Context) (string, bool) {
	k, ok := ctx.Value(requestKeyCtxKey{}).(string)
	return k, ok
}
