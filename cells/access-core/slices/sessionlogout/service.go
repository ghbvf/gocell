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
	outboxrt "github.com/ghbvf/gocell/runtime/outbox"
)

const (
	TopicSessionRevoked = "event.session.revoked.v1"
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

// persistRevoke wraps the session update + outbox write in a txRunner-aware call.
// When txRunner is nil (demo mode), fn is called directly without a transaction.
func (s *Service) persistRevoke(ctx context.Context, fn func(context.Context) error) error {
	if s.txRunner != nil {
		return s.txRunner.RunInTx(ctx, fn)
	}
	return fn(ctx)
}

// Logout revokes the caller's own session identified by sessionID.
//
// Ownership is enforced inside the repository query (RevokeByIDAndOwner)
// rather than a handler-side compare, eliminating the TOCTOU window that a
// load-then-check pattern leaves and preventing cross-user session enumeration
// (IDOR). A session that does not exist OR does not belong to the caller
// yields the same ErrSessionNotFound — the two cases are intentionally
// conflated per the Ory Kratos and Keycloak account/SessionResource pattern.
//
// Admin-side force logout belongs to a separate endpoint (not yet implemented);
// it must NOT reuse this method with a bypass flag.
func (s *Service) Logout(ctx context.Context, sessionID, callerUserID string) error {
	if sessionID == "" {
		return errcode.New(errcode.ErrAuthLogoutInvalidInput, "session ID is required")
	}
	if callerUserID == "" {
		return errcode.New(errcode.ErrAuthLogoutInvalidInput, "caller user ID is required")
	}

	payload, _ := json.Marshal(map[string]any{
		"session_id": sessionID, "user_id": callerUserID,
	})

	// Wrap the owner-scoped revoke + outbox write in a transaction for L2 atomicity.
	revokeAndPublish := func(txCtx context.Context) error {
		if err := s.sessionRepo.RevokeByIDAndOwner(txCtx, sessionID, callerUserID); err != nil {
			return err
		}
		if s.outboxWriter != nil {
			entry := outbox.Entry{
				ID:        outbox.NewEntryID(),
				EventType: TopicSessionRevoked,
				Payload:   payload,
			}
			if writeErr := s.outboxWriter.Write(txCtx, entry); writeErr != nil {
				return fmt.Errorf("session-logout: write outbox entry: %w", writeErr)
			}
		}
		return nil
	}

	if err := s.persistRevoke(ctx, revokeAndPublish); err != nil {
		return err
	}

	// Fallback direct publish when outbox is not in use. Wrap in v1 wire envelope
	// so the eventbus fail-closed schema check (P1-14) accepts the message.
	if s.outboxWriter == nil {
		envelope := outboxrt.MarshalDirectEnvelope(TopicSessionRevoked, TopicSessionRevoked, outbox.NewEntryID(), payload)
		if pubErr := s.publisher.Publish(ctx, TopicSessionRevoked, envelope); pubErr != nil {
			s.logger.Warn("session-logout: failed to publish event (demo mode)",
				slog.Any("error", pubErr),
				slog.String("topic", TopicSessionRevoked))
		}
	}

	s.logger.Info("session revoked",
		slog.String("session_id", sessionID), slog.String("user_id", callerUserID))
	return nil
}

// LogoutUser revokes all sessions for a user.
func (s *Service) LogoutUser(ctx context.Context, userID string) error {
	if userID == "" {
		return errcode.New(errcode.ErrAuthLogoutInvalidInput, "user ID is required")
	}

	if err := s.sessionRepo.RevokeByUserID(ctx, userID); err != nil {
		return fmt.Errorf("session-logout: revoke all: %w", err)
	}

	s.logger.Info("all sessions revoked for user", slog.String("user_id", userID))
	return nil
}
