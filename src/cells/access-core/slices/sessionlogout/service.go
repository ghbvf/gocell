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
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/google/uuid"
)

const (
	TopicSessionRevoked = "event.session.revoked.v1"

	ErrLogoutInvalidInput errcode.Code = "ERR_AUTH_LOGOUT_INVALID_INPUT"
)

// Option configures a session-logout Service.
type Option func(*Service)

// WithOutboxWriter sets the outbox.Writer for transactional event publishing.
func WithOutboxWriter(w outbox.Writer) Option {
	return func(s *Service) { s.outboxWriter = w }
}

// WithTxManager sets the TxRunner for transactional guarantees (L2 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) { s.txRunner = tx }
}

// Service implements session revocation.
type Service struct {
	sessionRepo  ports.SessionRepository
	publisher    outbox.Publisher
	outboxWriter outbox.Writer
	txRunner     persistence.TxRunner
	logger       *slog.Logger
}

// NewService creates a session-logout Service.
func NewService(
	sessionRepo ports.SessionRepository,
	pub outbox.Publisher,
	logger *slog.Logger,
	opts ...Option,
) *Service {
	s := &Service{
		sessionRepo: sessionRepo,
		publisher:   pub,
		logger:      logger,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Logout revokes a session by its ID.
func (s *Service) Logout(ctx context.Context, sessionID string) error {
	if sessionID == "" {
		return errcode.New(ErrLogoutInvalidInput, "session ID is required")
	}

	session, err := s.sessionRepo.GetByID(ctx, sessionID)
	if err != nil {
		return errcode.New(errcode.ErrSessionNotFound, "session not found")
	}

	if session.IsRevoked() {
		return nil // already revoked, idempotent
	}

	session.Revoke()

	// Publish event.
	payload, _ := json.Marshal(map[string]any{
		"session_id": sessionID, "user_id": session.UserID,
	})

	// Wrap session update + outbox write in a transaction for L2 atomicity.
	persistAndPublish := func(txCtx context.Context) error {
		if err := s.sessionRepo.Update(txCtx, session); err != nil {
			return fmt.Errorf("session-logout: persist revoke: %w", err)
		}
		if s.outboxWriter != nil {
			entry := outbox.Entry{
				ID:        "evt" + "-" + uuid.NewString(),
				EventType: TopicSessionRevoked,
				Payload:   payload,
			}
			if writeErr := s.outboxWriter.Write(txCtx, entry); writeErr != nil {
				return fmt.Errorf("session-logout: write outbox entry: %w", writeErr)
			}
		}
		return nil
	}

	if s.txRunner != nil {
		if err := s.txRunner.RunInTx(ctx, persistAndPublish); err != nil {
			return err
		}
	} else {
		if err := persistAndPublish(ctx); err != nil {
			return err
		}
	}

	// Fallback direct publish when outbox is not in use.
	if s.outboxWriter == nil {
		if pubErr := s.publisher.Publish(ctx, TopicSessionRevoked, payload); pubErr != nil {
			s.logger.Error("session-logout: failed to publish event",
				slog.Any("error", pubErr))
		}
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
