package idempotency

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
)

// RequestIdentity is the sealed, validated HTTP idempotency identity the middleware
// mints from a request. It is the single source the #1610 HTTP→command bridge
// (runtime/command.EmitAsyncFromIdempotencyKey) consumes to derive a command dedup
// key.
//
// # Sealed construction (AI-robust Hard)
//
// Unexported fields + the sole constructor NewRequestIdentity make "an unvalidated
// Idempotency-Key reaching the command dedup path" type-inexpressible: a
// RequestIdentity cannot be built outside this package without going through
// NewRequestIdentity, which enforces the full middleware-grade key validation
// (non-empty, ≤256 bytes, printable ASCII, brace-free). The middleware is the sole
// PRODUCTION minter; tests construct via NewRequestIdentity too. This subsumes the
// older raw-string ctx key (which let any caller inject an unvalidated key).
type RequestIdentity struct {
	caller      string // request principal subject (the requester)
	fingerprint string // request body fingerprint
	key         string // validated Idempotency-Key header value
}

// NewRequestIdentity is the SOLE constructor of a RequestIdentity. It validates key
// with the same rules the middleware applies at the request edge (validateKeyValue)
// and returns a KindInvalid error if the key is empty / oversized / non-printable /
// brace-bearing — so a malformed key can never enter the command dedup path. caller
// and fingerprint are opaque values the middleware supplies (the authenticated
// principal subject and a body content hash).
func NewRequestIdentity(caller, fingerprint, key string) (RequestIdentity, error) {
	if err := validateKeyValue(key); err != nil {
		return RequestIdentity{}, err
	}
	return RequestIdentity{caller: caller, fingerprint: fingerprint, key: key}, nil
}

// CommandDedupToken returns the opaque, NUL-free composite token that names one
// client operation attempt for command-layer dedup: a hex SHA-256 of
// caller + fingerprint + key. Two HTTP requests fold to the same command dedup slot
// IFF caller, payload fingerprint, AND Idempotency-Key all match — a genuine
// cross-cell / cross-pod duplicate. Different caller (same key) or different payload
// (same key) yield DIFFERENT tokens, so they are NOT silently deduplicated (the
// Stripe/IETF composite-key + distinct-different-payload posture, #1610 F1).
//
// Deliberately EXCLUDES the HTTP method+path: folding the SAME logical command
// submitted via DIFFERENT cells/endpoints into ONE slot is the whole point of
// #1610, so a path dimension would defeat cross-cell dedup. The operation content
// is already captured by the payload fingerprint, and the dedup aggregate (e.g.
// deviceID) + tenant are carried separately into DeriveCommandKey by the relay.
func (i RequestIdentity) CommandDedupToken() string {
	sum := sha256.Sum256([]byte(i.caller + "\x00" + i.fingerprint + "\x00" + i.key))
	return hex.EncodeToString(sum[:])
}

// requestIdentityCtxKey is the unexported context-key type carrying a RequestIdentity,
// preventing collisions with other packages' context keys.
type requestIdentityCtxKey struct{}

// WithRequestIdentity returns a child context carrying id. The HTTP idempotency
// middleware calls it after minting the identity so a downstream producer handler
// can derive a command dedup token via RequestIdentityFromContext.
func WithRequestIdentity(ctx context.Context, id RequestIdentity) context.Context {
	return context.WithValue(ctx, requestIdentityCtxKey{}, id)
}

// RequestIdentityFromContext returns the RequestIdentity the middleware injected,
// if present. ok=false when the request carried no Idempotency-Key, the route is
// idempotency-exempt, or the method is non-idempotent — the middleware injects only
// on intercepted, validated requests.
func RequestIdentityFromContext(ctx context.Context) (RequestIdentity, bool) {
	id, ok := ctx.Value(requestIdentityCtxKey{}).(RequestIdentity)
	return id, ok
}
