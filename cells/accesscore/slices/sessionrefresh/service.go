// Package sessionrefresh implements the session-refresh slice: validates an
// opaque refresh token via refresh.Store and issues a fresh access JWT.
package sessionrefresh

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/cells/accesscore/internal/sessionmint"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
)

// TokenPair holds the issued access and refresh tokens.
type TokenPair struct {
	AccessToken           string
	RefreshToken          string
	ExpiresAt             time.Time
	PasswordResetRequired bool
}

// Option configures a session-refresh Service. No built-in options exist today;
// the type is declared so future extensions are non-breaking (matches sessionlogin
// pattern, F8).
type Option func(*Service)

// Service implements token refresh logic.
type Service struct {
	sessionRepo  ports.SessionRepository
	userRepo     ports.UserRepository
	roleRepo     ports.RoleRepository
	refreshStore refresh.Store
	issuer       *auth.JWTIssuer
	logger       *slog.Logger
}

// NewService creates a session-refresh Service.
//
// userRepo is required (P1-3 fix): fetchPasswordResetRequired silently
// returns false when userRepo is nil, which bypasses the password-reset
// security gate.
//
// refreshStore owns both token-state validation and rotation — the slice
// no longer parses JWTs or performs application-layer reuse detection.
//
// opts allows future functional extensions without breaking callers (F8).
func NewService(
	sessionRepo ports.SessionRepository,
	roleRepo ports.RoleRepository,
	userRepo ports.UserRepository,
	refreshStore refresh.Store,
	issuer *auth.JWTIssuer,
	logger *slog.Logger,
	opts ...Option,
) *Service {
	if sessionRepo == nil {
		panic("sessionrefresh.NewService: sessionRepo must not be nil")
	}
	if roleRepo == nil {
		panic("sessionrefresh.NewService: roleRepo must not be nil")
	}
	if userRepo == nil {
		panic("sessionrefresh.NewService: userRepo must not be nil")
	}
	if refreshStore == nil {
		panic("sessionrefresh.NewService: refreshStore must not be nil")
	}
	if issuer == nil {
		panic("sessionrefresh.NewService: issuer must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		sessionRepo:  sessionRepo,
		roleRepo:     roleRepo,
		userRepo:     userRepo,
		refreshStore: refreshStore,
		issuer:       issuer,
		logger:       logger,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Refresh validates the presented opaque refresh token, checks the backing
// session and subject, mints a new access JWT, and only then commits refresh
// token rotation. Token rejection surfaces ErrAuthRefreshFailed; dependency
// failures surface ErrAuthRefreshUnavailable so clients do not confuse an
// outage with invalid credentials.
//
// Presenting an access JWT (or any string that does not parse as the opaque
// selector.verifier wire format) fails ParseOpaque inside refresh.Store and
// returns refresh.ErrRejected — the same fail-closed behaviour as
// before (TestAuthIntent_AccessTokenBlockedAtRefreshPath).
func (s *Service) Refresh(ctx context.Context, refreshToken string) (*TokenPair, error) {
	if err := validation.RequireNotBlank(errcode.ErrAuthRefreshInvalidInput,
		validation.F("refreshToken", refreshToken),
	); err != nil {
		return nil, err
	}

	presented, err := s.refreshStore.Peek(ctx, refreshToken)
	if err != nil {
		return nil, s.refreshStoreError("session-refresh: refresh store peek failed", err)
	}

	// Belt-and-braces: double-check the backing session has not been revoked
	// out-of-band (e.g. a logout that bypassed the refresh store). Session
	// verification is extracted to verifySession to keep cognitive complexity
	// within the ≤15 limit (F4/F5/gocognit).
	session, err := s.verifySession(ctx, presented.SessionID)
	if err != nil {
		return nil, err
	}
	if session.UserID != presented.SubjectID {
		s.cascadeRevoke(ctx, presented.SessionID, "subject-mismatch")
		return nil, errcode.New(errcode.ErrAuthRefreshFailed, "invalid refresh token")
	}

	passwordResetRequired, err := s.fetchPasswordResetRequired(ctx, session.ID, session.UserID)
	if err != nil {
		return nil, err
	}

	minted, err := sessionmint.MintAccess(ctx, sessionmint.Deps{
		Issuer:   s.issuer,
		RoleRepo: s.roleRepo,
	}, sessionmint.Request{
		UserID:                session.UserID,
		SessionID:             session.ID,
		PasswordResetRequired: passwordResetRequired,
	})
	if err != nil {
		s.logger.Error("session-refresh: token issuance failed",
			slog.Any("error", err),
			slog.String("user_id", session.UserID),
			slog.String("session_id", session.ID))
		return nil, err
	}

	newWire, rotated, err := s.refreshStore.Rotate(ctx, refreshToken)
	if err != nil {
		return nil, s.refreshStoreError("session-refresh: refresh store rotate failed", err)
	}
	if rotated.SessionID != session.ID || rotated.SubjectID != session.UserID {
		s.cascadeRevoke(ctx, session.ID, "rotated-subject-mismatch")
		return nil, errcode.New(errcode.ErrAuthRefreshFailed, "invalid refresh token")
	}

	s.logger.Info("token refreshed", slog.String("user_id", session.UserID))

	return &TokenPair{
		AccessToken:           minted.AccessToken,
		RefreshToken:          newWire,
		ExpiresAt:             minted.ExpiresAt,
		PasswordResetRequired: passwordResetRequired,
	}, nil
}

func (s *Service) refreshStoreError(logMessage string, err error) error {
	if errors.Is(err, refresh.ErrRejected) {
		return errcode.New(errcode.ErrAuthRefreshFailed, "invalid refresh token")
	}
	s.logger.Error(logMessage, slog.Any("error", err))
	return errcode.WrapInfra(errcode.ErrAuthRefreshUnavailable, "refresh store unavailable", err)
}

// verifySession checks that the session backing a rotated token is live and
// cascade-revokes the refresh chain if it is not. Extracted from Refresh to
// stay within the cognitive-complexity budget (F4/F5).
func (s *Service) verifySession(ctx context.Context, sessionID string) (*domain.Session, error) {
	session, err := s.sessionRepo.GetByID(ctx, sessionID)
	if err != nil {
		if errcode.IsInfraError(err) {
			s.logger.Error("session-refresh: infra error on session lookup",
				slog.Any("error", err), slog.String("session_id", sessionID))
			return nil, errcode.WrapInfra(errcode.ErrAuthRefreshUnavailable, "session lookup unavailable", err)
		}
		// F4: cascade-revoke on not-found; log the revoke error if it fails.
		s.cascadeRevoke(ctx, sessionID, "session-not-found")
		return nil, errcode.New(errcode.ErrAuthRefreshFailed, "session not found")
	}
	if session.IsRevoked() {
		// F4: cascade-revoke on already-revoked session.
		s.cascadeRevoke(ctx, sessionID, "revoked-session")
		return nil, errcode.New(errcode.ErrAuthRefreshFailed, "session has been revoked")
	}
	return session, nil
}

// cascadeRevoke calls RevokeSession and logs any error. reason is only used
// in the error log and is never exposed to callers (enumeration defence).
func (s *Service) cascadeRevoke(ctx context.Context, sessionID, reason string) {
	if err := s.refreshStore.RevokeSession(ctx, sessionID); err != nil {
		s.logger.Error("session-refresh: cascade revoke failed",
			slog.String("reason", reason),
			slog.Any("error", err),
			slog.String("session_id", sessionID))
	}
}

// fetchPasswordResetRequired reads the current PasswordResetRequired flag
// from the user repo. Fail-closed: any error returns ErrAuthRefreshFailed so
// the caller aborts refresh rather than signing a token that omits the
// password_reset_required claim.
func (s *Service) fetchPasswordResetRequired(ctx context.Context, sessionID, userID string) (bool, error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		s.logger.Error("session-refresh: failed to fetch user for reset flag (fail-closed)",
			slog.Any("error", err), slog.String("user_id", userID))
		if errcode.IsInfraError(err) {
			return false, errcode.WrapInfra(errcode.ErrAuthRefreshUnavailable, "session user unavailable", err)
		}
		s.cascadeRevoke(ctx, sessionID, "user-not-found")
		return false, errcode.New(errcode.ErrAuthRefreshFailed, "session user unavailable")
	}
	return user.PasswordResetRequired, nil
}
