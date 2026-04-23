// Package sessionlogout implements the session-logout slice: revokes sessions
// and publishes revocation events.
package sessionlogout

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
)

const (
	TopicSessionRevoked = "event.session.revoked.v1"
)

// Option configures a session-logout Service.
type Option func(*Service)

// WithEmitter sets the event emitter.
func WithEmitter(e outbox.Emitter) Option {
	return func(s *Service) {
		if e != nil {
			s.emitter = e
		}
	}
}

// WithTxManager sets the TxRunner for transactional guarantees (L2 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(s *Service) { s.txRunner = persistence.RunnerOrNoop(tx) }
}

// Service implements session revocation.
type Service struct {
	sessionRepo ports.SessionRepository
	txRunner    persistence.TxRunner
	emitter     outbox.Emitter
	logger      *slog.Logger
}

// NewService creates a session-logout Service.
func NewService(
	sessionRepo ports.SessionRepository,
	logger *slog.Logger,
	opts ...Option,
) *Service {
	s := &Service{
		sessionRepo: sessionRepo,
		txRunner:    persistence.NoopTxRunner{},
		emitter:     outbox.NewNoopEmitter(),
		logger:      logger,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// persistRevoke wraps the session update + event emit in a transaction runner.
func (s *Service) persistRevoke(ctx context.Context, fn func(context.Context) error) error {
	return s.txRunner.RunInTx(ctx, fn)
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
	if err := validation.RequireNotBlank(errcode.ErrAuthLogoutInvalidInput,
		validation.F("id", sessionID),
	); err != nil {
		return err
	}
	if callerUserID == "" {
		// callerUserID is derived from JWT claims by the auth middleware, not from
		// client input. A blank value indicates a server-side auth misconfiguration,
		// not a missing request field — expose a generic message to the client.
		return errcode.New(errcode.ErrAuthLogoutInvalidInput, "logout requires authenticated caller")
	}

	payload, _ := json.Marshal(map[string]any{
		"session_id": sessionID, "user_id": callerUserID,
	})

	// Wrap the owner-scoped revoke + outbox write in a transaction for L2 atomicity.
	revokeAndPublish := func(txCtx context.Context) error {
		if err := s.sessionRepo.RevokeByIDAndOwner(txCtx, sessionID, callerUserID); err != nil {
			return err
		}
		entry := outbox.Entry{
			ID:        outbox.NewEntryID(),
			EventType: TopicSessionRevoked,
			Payload:   payload,
		}
		if emitErr := s.emitter.Emit(txCtx, entry); emitErr != nil {
			return fmt.Errorf("session-logout: emit event: %w", emitErr)
		}
		return nil
	}

	if err := s.persistRevoke(ctx, revokeAndPublish); err != nil {
		return err
	}

	s.logger.Info("session revoked",
		slog.String("session_id", sessionID), slog.String("user_id", callerUserID))
	return nil
}

// LogoutUser revokes all sessions for a user.
func (s *Service) LogoutUser(ctx context.Context, userID string) error {
	if userID == "" {
		// userID is a server-derived value (event payload / JWT claim), not a
		// client-submitted field. Exposing the internal name would leak internals.
		return errcode.New(errcode.ErrAuthLogoutInvalidInput, "logout requires a valid user identifier")
	}

	if err := s.sessionRepo.RevokeByUserID(ctx, userID); err != nil {
		return fmt.Errorf("session-logout: revoke all: %w", err)
	}

	s.logger.Info("all sessions revoked for user", slog.String("user_id", userID))
	return nil
}
