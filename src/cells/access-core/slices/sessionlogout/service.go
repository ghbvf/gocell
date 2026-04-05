// Package sessionlogout implements the session-logout slice: revokes sessions
// and publishes revocation events.
package sessionlogout

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
)

const (
	TopicSessionRevoked = "event.session.revoked.v1"

	ErrLogoutInvalidInput errcode.Code = "ERR_AUTH_LOGOUT_INVALID_INPUT"
)

// Service implements session revocation.
type Service struct {
	sessionRepo ports.SessionRepository
	publisher   outbox.Publisher
	logger      *slog.Logger
}

// NewService creates a session-logout Service.
func NewService(
	sessionRepo ports.SessionRepository,
	pub outbox.Publisher,
	logger *slog.Logger,
) *Service {
	return &Service{
		sessionRepo: sessionRepo,
		publisher:   pub,
		logger:      logger,
	}
}

// Logout revokes a session by its ID.
func (s *Service) Logout(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return errcode.New(ErrLogoutInvalidInput, "session ID is required")
	}

	session, err := s.sessionRepo.GetByID(ctx, sessionID)
	if err != nil {
		return errcode.New("ERR_SESSION_NOT_FOUND", "session not found")
	}

	if session.IsRevoked() {
		return nil // already revoked, idempotent
	}

	session.Revoke()

	if err := s.sessionRepo.Update(ctx, session); err != nil {
		return fmt.Errorf("session-logout: persist revoke: %w", err)
	}

	// Publish event.
	payload, _ := json.Marshal(map[string]any{
		"session_id": sessionID, "user_id": session.UserID,
	})
	if pubErr := s.publisher.Publish(ctx, TopicSessionRevoked, payload); pubErr != nil {
		s.logger.Error("session-logout: failed to publish event",
			slog.Any("error", pubErr))
	}

	s.logger.Info("session revoked",
		slog.String("session_id", sessionID), slog.String("user_id", session.UserID))
	return nil
}

// LogoutUser revokes all sessions for a user.
func (s *Service) LogoutUser(ctx context.Context, userID string) error {
	if userID == "" {
		return errcode.New(ErrLogoutInvalidInput, "user ID is required")
	}

	if err := s.sessionRepo.RevokeByUserID(ctx, userID); err != nil {
		return fmt.Errorf("session-logout: revoke all: %w", err)
	}

	s.logger.Info("all sessions revoked for user", slog.String("user_id", userID))
	return nil
}
