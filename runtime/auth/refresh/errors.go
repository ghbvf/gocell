// This file intentionally declares only one sentinel (ErrRejected).
// Do NOT add ErrTokenExpired, ErrTokenRevoked, ErrTokenReused, etc. —
// enumeration of rejection causes is a timing / side-channel oracle.
// All diagnostic reasons surface through the structured slog field "reason".
package refresh

import "github.com/ghbvf/gocell/pkg/errcode"

// ErrRejected is the single sentinel returned by Store implementations for
// every unhappy Peek/Rotate path (malformed wire, unknown selector, verifier
// mismatch, expired, revoked, reused beyond grace). Callers map it to
// HTTP 401 via pkg/httputil and must not distinguish between causes — the
// distinction is observable through the structured slog field "reason",
// never through the error shape.
//
// Rationale: collapsing NotFound/Expired/Revoked/Reused into one public
// sentinel eliminates enumeration and timing side-channels in the refresh
// endpoint. Operations retain full diagnostic fidelity through the slog
// "reason" attribute (malformed | selector_miss | verifier_miss |
// expired | revoked | rotated_beyond_grace | reuse_detected).
//
// CategoryAuth — the HTTP 401 mapping is enforced by pkg/errcode/classify.go.
var ErrRejected = errcode.New(
	errcode.KindUnauthenticated,
	errcode.ErrRefreshTokenRejected,
	"refresh token rejected",
	errcode.WithCategory(errcode.CategoryAuth),
)
