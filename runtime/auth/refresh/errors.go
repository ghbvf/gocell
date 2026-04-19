package refresh

import "github.com/ghbvf/gocell/pkg/errcode"

// Sentinel errors returned by Store implementations.
//
// ErrTokenReused is CategoryAuth (attack signal per OAuth2 RFC 6749 §10.4);
// all other sentinels are CategoryDomain for normal client-observable
// conditions. This split allows monitoring systems to page on reuse events
// while treating expiry / revoke as expected business conditions.
//
// ref: F2 contract C3 from docs/plans/202604191515-auth-federated-whistle.md
var (
	// ErrTokenNotFound is returned when the presented token does not exist in
	// the active token set and is not found as an obsolete token either.
	ErrTokenNotFound = errcode.NewDomain(errcode.ErrRefreshTokenNotFound, "refresh token not found")

	// ErrTokenExpired is returned when the token's ExpiresAt is in the past.
	ErrTokenExpired = errcode.NewDomain(errcode.ErrRefreshTokenExpired, "refresh token expired")

	// ErrTokenRevoked is returned when Rotate is called on a token whose
	// session has already been revoked (revoked_at IS NOT NULL).
	ErrTokenRevoked = errcode.NewDomain(errcode.ErrRefreshTokenRevoked, "refresh token revoked")

	// ErrTokenReused is returned when an obsolete token is presented outside
	// the ReuseInterval grace window. The store cascades Revoke(sessionID)
	// before returning this error — callers must treat this as a security event.
	//
	// errcode.IsInfraError(ErrTokenReused) == false (CategoryAuth is not infra).
	ErrTokenReused = errcode.NewAuth(errcode.ErrRefreshTokenReused, "refresh token reuse detected")
)
