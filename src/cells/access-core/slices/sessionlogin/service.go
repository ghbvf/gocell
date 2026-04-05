// Package sessionlogin implements the session-login slice: password-based login
// with JWT issuance.
package sessionlogin

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/id"
)

const (
	TopicSessionCreated = "event.session.created.v1"

	ErrLoginInvalidInput errcode.Code = "ERR_AUTH_LOGIN_INVALID_INPUT"
	ErrLoginFailed       errcode.Code = "ERR_AUTH_LOGIN_FAILED"

	accessTokenTTL  = 15 * time.Minute
	refreshTokenTTL = 7 * 24 * time.Hour
)

// TokenPair holds the issued access and refresh tokens.
type TokenPair struct {
	AccessToken  string    `json:"accessToken"`
	RefreshToken string    `json:"refreshToken"`
	ExpiresAt    time.Time `json:"expiresAt"`
}

// Service implements password login with JWT issuance.
type Service struct {
	userRepo    ports.UserRepository
	sessionRepo ports.SessionRepository
	roleRepo    ports.RoleRepository
	publisher   outbox.Publisher
	signingKey  []byte
	logger      *slog.Logger
}

// NewService creates a session-login Service.
func NewService(
	userRepo ports.UserRepository,
	sessionRepo ports.SessionRepository,
	roleRepo ports.RoleRepository,
	pub outbox.Publisher,
	signingKey []byte,
	logger *slog.Logger,
) *Service {
	return &Service{
		userRepo:    userRepo,
		sessionRepo: sessionRepo,
		roleRepo:    roleRepo,
		publisher:   pub,
		signingKey:  signingKey,
		logger:      logger,
	}
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

	// Issue JWT.
	now := time.Now()
	expiresAt := now.Add(accessTokenTTL)
	sessionID := id.New("sess")

	accessToken, err := s.issueToken(user.ID, roleNames, expiresAt, sessionID)
	if err != nil {
		return nil, fmt.Errorf("session-login: issue access token: %w", err)
	}

	refreshExpiry := now.Add(refreshTokenTTL)
	refreshToken, err := s.issueToken(user.ID, nil, refreshExpiry, "")
	if err != nil {
		return nil, fmt.Errorf("session-login: issue refresh token: %w", err)
	}

	// Persist session.
	session, err := domain.NewSession(user.ID, accessToken, refreshToken, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("session-login: create session: %w", err)
	}
	session.ID = sessionID

	if err := s.sessionRepo.Create(ctx, session); err != nil {
		return nil, fmt.Errorf("session-login: persist session: %w", err)
	}

	// Publish event.
	payload, _ := json.Marshal(map[string]any{
		"session_id": session.ID, "user_id": user.ID,
	})
	if pubErr := s.publisher.Publish(ctx, TopicSessionCreated, payload); pubErr != nil {
		s.logger.Error("session-login: failed to publish event",
			slog.Any("error", pubErr))
	}

	s.logger.Info("user logged in",
		slog.String("user_id", user.ID), slog.String("session_id", session.ID))
	return &TokenPair{
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		ExpiresAt:    expiresAt,
	}, nil
}

func (s *Service) issueToken(subject string, roles []string, expiresAt time.Time, sid string) (string, error) {
	claims := jwt.MapClaims{
		"sub": subject,
		"iat": jwt.NewNumericDate(time.Now()),
		"exp": jwt.NewNumericDate(expiresAt),
		"iss": "gocell-access-core",
		"aud": jwt.ClaimStrings{"gocell"},
	}
	if len(roles) > 0 {
		claims["roles"] = roles
	}
	if sid != "" {
		claims["sid"] = sid
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(s.signingKey)
}
