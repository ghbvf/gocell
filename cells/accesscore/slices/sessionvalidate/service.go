// Package sessionvalidate implements the session-validate slice: verifies
// access tokens and returns Claims. Implements runtime/auth.IntentTokenVerifier.
package sessionvalidate

import (
	"context"
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// errMsgAuthFailed is the uniform error message for all session validation
// failures. Using a single message prevents session-state enumeration attacks.
const errMsgAuthFailed = "invalid or expired authentication token"

// errMsgServiceUnavailable is the uniform error message when an infrastructure
// dependency (session store or user repo) is temporarily unreachable.
const errMsgServiceUnavailable = "authentication service unavailable"

// Compile-time check: Service satisfies runtime/auth.IntentTokenVerifier so it
// can be plugged into AuthMiddleware (which now demands intent-aware verifiers
// by signature).
var _ auth.IntentTokenVerifier = (*Service)(nil)

// Service validates JWT access tokens and checks session revocation status.
type Service struct {
	verifier     auth.IntentTokenVerifier
	sessionStore session.Store
	userRepo     ports.UserRepository
	logger       *slog.Logger
}

// NewService creates a session-validate Service. Returns an error when any
// required dependency is nil.
func NewService(
	verifier auth.IntentTokenVerifier,
	sessionStore session.Store,
	userRepo ports.UserRepository,
	logger *slog.Logger,
) (*Service, error) {
	if userRepo == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"session-validate: UserRepository required")
	}
	return &Service{verifier: verifier, sessionStore: sessionStore, userRepo: userRepo, logger: logger}, nil
}

// VerifyIntent validates an access token. This service is intentionally
// scoped to access tokens (session-revocation checks presume a business
// endpoint), so any expected intent other than TokenIntentAccess is rejected
// as ErrAuthInvalidTokenIntent. Callers needing refresh-token validation must
// use the underlying JWTVerifier directly (see sessionrefresh).
func (s *Service) VerifyIntent(ctx context.Context, tokenStr string, expected auth.TokenIntent) (auth.Claims, error) {
	if expected != auth.TokenIntentAccess {
		s.logger.Warn("session-validate: unsupported intent",
			slog.String("expected", string(expected)))
		return auth.Claims{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthInvalidTokenIntent, errMsgAuthFailed)
	}
	claims, err := s.verifyJWTWithIntent(ctx, tokenStr)
	if err != nil {
		return auth.Claims{}, err
	}
	if s.sessionStore == nil {
		return claims, nil
	}
	return s.enforceSessionState(ctx, claims)
}

// verifyJWTWithIntent runs the underlying verifier enforcing token_use=access
// at both the claim and JOSE header level, mapping all failures to the uniform
// ErrAuthInvalidToken response to prevent token-type enumeration.
func (s *Service) verifyJWTWithIntent(ctx context.Context, tokenStr string) (auth.Claims, error) {
	claims, err := s.verifier.VerifyIntent(ctx, tokenStr, auth.TokenIntentAccess)
	if err != nil {
		s.logger.Warn("session-validate: JWT verification failed",
			slog.Any("error", err))
		return auth.Claims{}, errcode.Wrap(errcode.KindUnauthenticated, errcode.ErrAuthInvalidToken, errMsgAuthFailed, err)
	}
	return claims, nil
}

// enforceSessionState performs session-revocation and epoch-invariant checks
// that follow a successful JWT verification. Tokens missing the sid claim are
// rejected when sessionStore is configured (fail-closed).
//
// Two sequential reads are performed under READ COMMITTED isolation with no
// snapshot guarantee (plan decision HIGH-4 — no read-only tx wrap).
func (s *Service) enforceSessionState(ctx context.Context, claims auth.Claims) (auth.Claims, error) {
	sid := claims.SessionID
	if sid == "" {
		s.logger.Warn("session-validate: token missing sid",
			slog.String("subject", claims.Subject))
		return auth.Claims{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthInvalidToken, errMsgAuthFailed)
	}

	// 1) Session row exists and is not revoked.
	view, err := s.sessionStore.Get(ctx, sid)
	if err != nil {
		if errcode.IsInfraError(err) {
			s.logger.Error("session-validate: session store unavailable",
				slog.String("sid", sid),
				slog.String("subject", claims.Subject),
				slog.Any("error", err))
			return auth.Claims{}, errcode.Wrap(errcode.KindUnavailable, errcode.ErrAuthServiceUnavailable,
				errMsgServiceUnavailable, err)
		}
		s.logSessionLookupError(sid, claims.Subject, err)
		return auth.Claims{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthInvalidToken, errMsgAuthFailed)
	}
	if view.RevokedAt != nil {
		s.logger.Warn("session-validate: revoked session used",
			slog.String("sid", sid),
			slog.String("subject", claims.Subject))
		return auth.Claims{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthInvalidToken, errMsgAuthFailed)
	}

	// 2) Epoch invariant: user.authz_epoch must not exceed claims.AuthzEpoch.
	user, err := s.userRepo.GetByID(ctx, claims.Subject)
	if err != nil {
		if errcode.IsInfraError(err) {
			s.logger.Error("session-validate: user repo unavailable",
				slog.String("subject", claims.Subject),
				slog.Any("error", err))
			return auth.Claims{}, errcode.Wrap(errcode.KindUnavailable, errcode.ErrAuthServiceUnavailable,
				errMsgServiceUnavailable, err)
		}
		// Domain not-found: subject deleted or never existed → uniform 401.
		s.logger.Warn("session-validate: subject not found",
			slog.String("subject", claims.Subject))
		return auth.Claims{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthInvalidToken, errMsgAuthFailed)
	}
	if user.AuthzEpoch > claims.AuthzEpoch {
		s.logger.Warn("session-validate: authz epoch mismatch",
			slog.String("subject", claims.Subject),
			slog.Int64("user_epoch", user.AuthzEpoch),
			slog.Int64("claim_epoch", claims.AuthzEpoch))
		return auth.Claims{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthInvalidToken, errMsgAuthFailed)
	}

	return claims, nil
}

// logSessionLookupError distinguishes "not found" (expected / logged at Warn)
// from infrastructure failures (Error) so dashboards can alert correctly.
//
// Only domain-layer not-found codes on the whitelist (ErrSessionNotFound) are
// logged at Warn. Any infra error, unclassified error, or non-whitelisted
// errcode is logged at Error — fail-closed, ref S40.
func (s *Service) logSessionLookupError(sid, subject string, err error) {
	if errcode.IsDomainNotFound(err, errcode.ErrSessionNotFound) {
		s.logger.Warn("session-validate: session not found",
			slog.String("sid", sid),
			slog.String("subject", subject))
		return
	}
	s.logger.Error("session-validate: session repo unavailable",
		slog.String("sid", sid),
		slog.String("subject", subject),
		slog.Any("error", err))
}
