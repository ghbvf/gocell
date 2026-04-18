// Package sessionlogin implements the session-login slice: password-based login
// with JWT issuance.
package sessionlogin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/dto"
	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	outboxrt "github.com/ghbvf/gocell/runtime/outbox"
	"github.com/google/uuid"
)

const TopicSessionCreated = "event.session.created.v1"

// TokenPair holds the issued access and refresh tokens.
type TokenPair struct {
	AccessToken           string
	RefreshToken          string
	ExpiresAt             time.Time
	SessionID             string
	PasswordResetRequired bool
}

// Option configures a session-login Service.
type Option func(*Service)

// WithOutboxWriter sets the outbox.Writer for transactional event publishing.
func WithOutboxWriter(w outbox.Writer) Option {
	return func(s *Service) { s.outboxWriter = w }
}

// WithTxManager sets the TxRunner for transactional guarantees (L2 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) { s.txRunner = tx }
}

// Service implements password login with JWT issuance.
type Service struct {
	userRepo     ports.UserRepository
	sessionRepo  ports.SessionRepository
	roleRepo     ports.RoleRepository
	publisher    outbox.Publisher
	outboxWriter outbox.Writer
	txRunner     persistence.TxRunner
	issuer       *auth.JWTIssuer
	audience     []string
	logger       *slog.Logger
}

// NewService creates a session-login Service.
// The audience for issued tokens is read from issuer.DefaultAudience() at
// construction time so there is no hard-coded audience constant in this slice.
func NewService(
	userRepo ports.UserRepository,
	sessionRepo ports.SessionRepository,
	roleRepo ports.RoleRepository,
	pub outbox.Publisher,
	issuer *auth.JWTIssuer,
	logger *slog.Logger,
	opts ...Option,
) *Service {
	s := &Service{
		userRepo:    userRepo,
		sessionRepo: sessionRepo,
		roleRepo:    roleRepo,
		publisher:   pub,
		issuer:      issuer,
		audience:    issuer.DefaultAudience(),
		logger:      logger,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// LoginInput holds login parameters.
type LoginInput struct {
	Username string
	Password string
}

// Login authenticates a user and returns a JWT token pair.
func (s *Service) Login(ctx context.Context, input LoginInput) (*TokenPair, error) {
	if input.Username == "" || input.Password == "" {
		return nil, errcode.New(errcode.ErrAuthLoginInvalidInput, "username and password are required")
	}

	user, err := s.userRepo.GetByUsername(ctx, input.Username)
	if err != nil {
		return nil, errcode.New(errcode.ErrAuthLoginFailed, "invalid credentials")
	}

	if user.IsLocked() {
		return nil, errcode.New(errcode.ErrAuthUserLocked, "account is locked")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(input.Password)); err != nil {
		return nil, errcode.New(errcode.ErrAuthLoginFailed, "invalid credentials")
	}

	roleNames, err := s.fetchRoleNames(ctx, user.ID)
	if err != nil {
		s.logger.Warn("session-login: failed to fetch roles",
			slog.Any("error", err), slog.String("user_id", user.ID))
	}

	now := time.Now()
	expiresAt := now.Add(auth.DefaultAccessTokenTTL)
	sessionID := "sess" + "-" + uuid.NewString()

	accessToken, err := s.issueAccessToken(user.ID, roleNames, sessionID, user.PasswordResetRequired)
	if err != nil {
		return nil, fmt.Errorf("session-login: issue access token: %w", err)
	}

	refreshToken, err := s.issueRefreshToken(user.ID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("session-login: issue refresh token: %w", err)
	}

	session, err := domain.NewSession(user.ID, accessToken, refreshToken, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("session-login: create session: %w", err)
	}
	session.ID = sessionID

	payload, _ := json.Marshal(map[string]any{
		"session_id": session.ID, "user_id": user.ID,
	})

	if err := s.persistSession(ctx, session, payload); err != nil {
		return nil, err
	}

	s.maybePublishDirect(ctx, payload)

	s.logger.Info("user logged in",
		slog.String("user_id", user.ID), slog.String("session_id", session.ID))
	return &TokenPair{
		AccessToken:           accessToken,
		RefreshToken:          refreshToken,
		ExpiresAt:             expiresAt,
		SessionID:             sessionID,
		PasswordResetRequired: user.PasswordResetRequired,
	}, nil
}

// fetchRoleNames returns the role names for the given user ID.
func (s *Service) fetchRoleNames(ctx context.Context, userID string) ([]string, error) {
	roles, err := s.roleRepo.GetByUserID(ctx, userID)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(roles))
	for _, r := range roles {
		names = append(names, r.Name)
	}
	return names, nil
}

// persistSession writes the session and (when configured) the outbox entry,
// optionally wrapped in a transaction when txRunner is available.
func (s *Service) persistSession(ctx context.Context, session *domain.Session, payload []byte) error {
	do := func(txCtx context.Context) error {
		if err := s.sessionRepo.Create(txCtx, session); err != nil {
			return fmt.Errorf("session-login: persist session: %w", err)
		}
		return s.writeOutboxEntry(txCtx, payload)
	}
	if s.txRunner != nil {
		return s.txRunner.RunInTx(ctx, do)
	}
	return do(ctx)
}

// writeOutboxEntry writes a session.created outbox entry when the writer is configured.
func (s *Service) writeOutboxEntry(ctx context.Context, payload []byte) error {
	if s.outboxWriter == nil {
		return nil
	}
	entry := outbox.Entry{
		ID:        outbox.NewEntryID(),
		EventType: TopicSessionCreated,
		Payload:   payload,
	}
	if err := s.outboxWriter.Write(ctx, entry); err != nil {
		return fmt.Errorf("session-login: write outbox entry: %w", err)
	}
	return nil
}

// maybePublishDirect publishes directly when outbox is not in use (demo mode).
// Wraps the business payload in a v1 wire envelope so the eventbus fail-closed
// schema check (P1-14) accepts the message and subscribers receive it.
func (s *Service) maybePublishDirect(ctx context.Context, payload []byte) {
	if s.outboxWriter != nil {
		return
	}
	envelope := outboxrt.MarshalDirectEnvelope(TopicSessionCreated, TopicSessionCreated, outbox.NewEntryID(), payload)
	if pubErr := s.publisher.Publish(ctx, TopicSessionCreated, envelope); pubErr != nil {
		s.logger.Warn("session-login: failed to publish event (demo mode)",
			slog.Any("error", pubErr),
			slog.String("topic", TopicSessionCreated))
	}
}

// issueAccessToken signs a short-lived JWT with intent=access for calling
// business endpoints. Access tokens carry roles for RBAC decisions and the
// passwordResetRequired flag so middleware can enforce server-side reset.
// The audience is sourced from s.audience (populated from issuer.DefaultAudience()
// at construction) — no hard-coded audience constant.
func (s *Service) issueAccessToken(subject string, roles []string, sessionID string, passwordResetRequired bool) (string, error) {
	return s.issuer.Issue(auth.TokenIntentAccess, subject, auth.IssueOptions{
		Roles:                 roles,
		Audience:              s.audience,
		SessionID:             sessionID,
		PasswordResetRequired: passwordResetRequired,
	})
}

// IssueForUser issues a fresh token pair for a user by ID. It re-fetches the
// user and their roles so the returned tokens reflect the current state (e.g.
// after ChangePassword clears PasswordResetRequired). Used by identitymanage
// ChangePassword to issue a replacement token pair without forcing a re-login.
//
// A new Session record is persisted to sessionRepo so that sessionvalidate can
// look up the session by its sid claim and enforce revocation/expiry. Without
// this step, sessionvalidate.enforceSessionState fails with "not found" → 401
// on the very next authenticated request (root cause of PR#183 round-2 CI failure).
//
// Returns *dto.TokenPair (internal/dto) so this method implements the
// identitymanage.TokenIssuer interface without a cross-slice import (F-ARCH-1).
func (s *Service) IssueForUser(ctx context.Context, userID string) (*dto.TokenPair, error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return nil, fmt.Errorf("session-login: IssueForUser get user: %w", err)
	}

	roleNames, err := s.fetchRoleNames(ctx, userID)
	if err != nil {
		s.logger.Warn("session-login: IssueForUser failed to fetch roles",
			slog.Any("error", err), slog.String("user_id", userID))
	}

	sessionID := "sess" + "-" + uuid.NewString()
	expiresAt := time.Now().Add(auth.DefaultAccessTokenTTL)

	accessToken, err := s.issueAccessToken(userID, roleNames, sessionID, user.PasswordResetRequired)
	if err != nil {
		return nil, fmt.Errorf("session-login: IssueForUser access token: %w", err)
	}

	refreshToken, err := s.issueRefreshToken(userID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("session-login: IssueForUser refresh token: %w", err)
	}

	// Persist the session so sessionvalidate can look it up by sid claim.
	// Without this, the token carries a sessionID that has no backing row, and
	// every subsequent request fails with 401 (session not found).
	session, err := domain.NewSession(userID, accessToken, refreshToken, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("session-login: IssueForUser create session: %w", err)
	}
	session.ID = sessionID
	if err := s.sessionRepo.Create(ctx, session); err != nil {
		return nil, fmt.Errorf("session-login: IssueForUser persist session: %w", err)
	}

	s.logger.Info("session-login: IssueForUser issued new session",
		slog.String("user_id", userID), slog.String("session_id", sessionID))

	return &dto.TokenPair{
		AccessToken:           accessToken,
		RefreshToken:          refreshToken,
		ExpiresAt:             expiresAt,
		SessionID:             sessionID,
		PasswordResetRequired: user.PasswordResetRequired,
	}, nil
}

// issueRefreshToken signs a longer-lived JWT with intent=refresh. Refresh
// tokens do not carry roles: they are consumed only by /auth/refresh, which
// looks up the current roles from the session's user on each rotation.
// The audience is sourced from s.audience (populated from issuer.DefaultAudience()
// at construction) — no hard-coded audience constant.
func (s *Service) issueRefreshToken(subject, sessionID string) (string, error) {
	return s.issuer.Issue(auth.TokenIntentRefresh, subject, auth.IssueOptions{
		Audience:  s.audience,
		SessionID: sessionID,
	})
}
