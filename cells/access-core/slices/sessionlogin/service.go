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
	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/google/uuid"
)

const TopicSessionCreated = "event.session.created.v1"

// TokenPair holds the issued access and refresh tokens.
type TokenPair struct {
	AccessToken  string
	RefreshToken string
	ExpiresAt    time.Time
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
	logger       *slog.Logger
}

// NewService creates a session-login Service.
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

	accessToken, err := s.issueAccessToken(user.ID, roleNames, sessionID)
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
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
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
		ID:        "evt" + "-" + uuid.NewString(),
		EventType: TopicSessionCreated,
		Payload:   payload,
	}
	if err := s.outboxWriter.Write(ctx, entry); err != nil {
		return fmt.Errorf("session-login: write outbox entry: %w", err)
	}
	return nil
}

// maybePublishDirect publishes directly when outbox is not in use (demo mode).
func (s *Service) maybePublishDirect(ctx context.Context, payload []byte) {
	if s.outboxWriter != nil {
		return
	}
	if pubErr := s.publisher.Publish(ctx, TopicSessionCreated, payload); pubErr != nil {
		s.logger.Warn("session-login: failed to publish event (demo mode)",
			slog.Any("error", pubErr),
			slog.String("topic", TopicSessionCreated))
	}
}

// issueAccessToken signs a short-lived JWT with intent=access for calling
// business endpoints. Access tokens carry roles for RBAC decisions.
func (s *Service) issueAccessToken(subject string, roles []string, sessionID string) (string, error) {
	return s.issuer.Issue(auth.TokenIntentAccess, subject, roles, []string{auth.DefaultJWTAudience}, sessionID)
}

// issueRefreshToken signs a longer-lived JWT with intent=refresh. Refresh
// tokens do not carry roles: they are consumed only by /auth/refresh, which
// looks up the current roles from the session's user on each rotation.
func (s *Service) issueRefreshToken(subject, sessionID string) (string, error) {
	return s.issuer.Issue(auth.TokenIntentRefresh, subject, nil, []string{auth.DefaultJWTAudience}, sessionID)
}
