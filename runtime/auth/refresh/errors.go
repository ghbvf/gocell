// This file declares the two public error sentinels for refresh.Store.
//
// Do NOT add ErrTokenExpired, ErrTokenRevoked, etc. — enumeration of
// rejection causes beyond the reuse / non-reuse split is a timing /
// side-channel oracle. All other diagnostic reasons surface through the
// structured slog field "reason".
//
// ErrReused is intentionally NOT a subtype of ErrRejected (no wrapping);
// callers must use errors.Is(err, ErrReused) or errors.Is(err, ErrRejected)
// as separate, mutually-exclusive branches. See godoc below.
package refresh

import "github.com/ghbvf/gocell/pkg/errcode"

// ErrRejected is the sentinel returned by Store implementations for every
// unhappy Peek/Rotate path that is NOT a reuse attack (malformed wire,
// unknown selector, verifier mismatch, expired, revoked). Callers map it to
// HTTP 401 via pkg/httputil and must not distinguish between causes — the
// distinction is observable through the structured slog field "reason",
// never through the error shape.
//
// Rationale: collapsing NotFound/Expired/Revoked into one public sentinel
// eliminates enumeration and timing side-channels in the refresh endpoint.
// Operations retain full diagnostic fidelity through the slog "reason"
// attribute (malformed | selector_miss | verifier_miss | expired | revoked |
// rotated_beyond_grace | idle_expired).
//
// CategoryAuth — the HTTP 401 mapping is enforced by pkg/errcode/classify.go.
//
// errors.Is chain: ErrRejected does NOT wrap ErrReused. The two sentinels
// are independent; errors.Is(ErrRejected, ErrReused) == false.
var ErrRejected = errcode.New(
	errcode.KindUnauthenticated,
	errcode.ErrRefreshTokenRejected,
	"refresh token rejected",
	errcode.WithCategory(errcode.CategoryAuth),
)

// ErrReused is returned by Store implementations when a refresh token that
// has already been consumed (rotated_at IS NOT NULL, i.e. it was previously
// rotated) is re-presented beyond the Policy.ReuseInterval grace window.
// This is a confirmed token-reuse attack signal.
//
// Callers MUST use errors.Is(err, ErrReused) to detect the reuse case and
// trigger cascade revoke + epoch bump. Other rejection causes (malformed,
// expired, revoked by logout) return ErrRejected and must NOT trigger the
// cascade-revoke side-effect.
//
// errors.Is chain: ErrReused does NOT wrap ErrRejected. The two sentinels
// are independent:
//
//	errors.Is(err, ErrReused)   == true   → confirmed reuse attack
//	errors.Is(err, ErrRejected) == false  → not a plain rejection
//
// CategoryAuth — maps to HTTP 401 like ErrRejected.
var ErrReused = errcode.New(
	errcode.KindUnauthenticated,
	errcode.ErrRefreshTokenReused,
	"refresh token reused",
	errcode.WithCategory(errcode.CategoryAuth),
)
