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
	"github.com/google/uuid"
	"github.com/ghbvf/gocell/runtime/auth"
)

const (
	TopicSessionCreated = "event.session.created.v1"

	ErrLoginInvalidInput errcode.Code = "ERR_AUTH_LOGIN_INVALID_INPUT"
	ErrLoginFailed       errcode.Code = "ERR_AUTH_LOGIN_FAILED"

	accessTokenTTL = 15 * time.Minute
)

// TokenPair holds the issued access and refresh tokens.
type TokenPair struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
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
		return nil, errcode.New(ErrLoginInvalidInput, "username and password are required")
	}

	user, err := s.userRepo.GetByUsername(ctx, input.Username)
	if err != nil {
		return nil, errcode.New(ErrLoginFailed, "invalid credentials")
	}

	if user.IsLocked() {
		return nil, errcode.New(domain.ErrUserLocked, "account is locked")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(input.Password)); err != nil {
		return nil, errcode.New(ErrLoginFailed, "invalid credentials")
	}

	// Fetch roles for JWT claims.
	roles, err := s.roleRepo.GetByUserID(ctx, user.ID)
	if err != nil {
		s.logger.Warn("session-login: failed to fetch roles",
			slog.Any("error", err), slog.String("user_id", user.ID))
	}
	roleNames := make([]string, 0, len(roles))
	for _, r := range roles {
		roleNames = append(roleNames, r.Name)
	}

	// Issue JWT via RS256 issuer.
	now := time.Now()
	expiresAt := now.Add(accessTokenTTL)
	sessionID := "sess" + "-" + uuid.NewString()

	accessToken, err := s.issueToken(user.ID, roleNames)
	if err != nil {
		return nil, fmt.Errorf("session-login: issue access token: %w", err)
	}

	refreshToken, err := s.issueToken(user.ID, nil)
	if err != nil {
		return nil, fmt.Errorf("session-login: issue refresh token: %w", err)
	}

	// Persist session.
	session, err := domain.NewSession(user.ID, accessToken, refreshToken, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("session-login: create session: %w", err)
	}
	session.ID = sessionID

	// Publish event.
	payload, _ := json.Marshal(map[string]any{
		"session_id": session.ID, "user_id": user.ID,
	})

	// Wrap session create + outbox write in a transaction for L2 atomicity.
	persistAndPublish := func(txCtx context.Context) error {
		if err := s.sessionRepo.Create(txCtx, session); err != nil {
			return fmt.Errorf("session-login: persist session: %w", err)
		}
		if s.outboxWriter != nil {
			entry := outbox.Entry{
				ID:        "evt" + "-" + uuid.NewString(),
				EventType: TopicSessionCreated,
				Payload:   payload,
			}
			if writeErr := s.outboxWriter.Write(txCtx, entry); writeErr != nil {
				return fmt.Errorf("session-login: write outbox entry: %w", writeErr)
			}
		}
		return nil
	}

	if s.txRunner != nil {
		if err := s.txRunner.RunInTx(ctx, persistAndPublish); err != nil {
			return nil, err
		}
	} else {
		if err := persistAndPublish(ctx); err != nil {
			return nil, err
		}
	}

	// Fallback direct publish when outbox is not in use.
	if s.outboxWriter == nil {
		if pubErr := s.publisher.Publish(ctx, TopicSessionCreated, payload); pubErr != nil {
			s.logger.Error("session-login: failed to publish event",
				slog.Any("error", pubErr))
		}
	}

	s.logger.Info("user logged in",
		slog.String("user_id", user.ID), slog.String("session_id", session.ID))
	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
	}, nil
}

func (s *Service) issueToken(subject string, roles []string) (string, error) {
	return s.issuer.Issue(subject, roles, []string{"gocell"})
}
