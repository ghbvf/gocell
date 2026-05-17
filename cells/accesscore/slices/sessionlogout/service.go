// Package sessionlogout implements the session-logout slice: revokes sessions
// and publishes revocation events.
package sessionlogout

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/session"
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

// WithTxManager sets the CellTxManager for transactional guarantees (L2
// atomicity). Callers obtain the sealed marker via persistence.WrapForCell
// from a composition root.
func WithTxManager(tx persistence.CellTxManager) Option {
	return func(s *Service) {
		if tx != nil {
			s.txRunner = tx
		}
	}
}

// Service implements session revocation.
type Service struct {
	sessionStore session.Store
	refreshStore refresh.Store
	txRunner     persistence.CellTxManager
	emitter      outbox.Emitter
	logger       *slog.Logger
}

// NewService creates a session-logout Service. refreshStore is required so
// that logout also revokes the refresh-token chain for the session — without
// this, a stolen refresh token would survive logout.
func NewService(
	sessionStore session.Store,
	refreshStore refresh.Store,
	logger *slog.Logger,
	opts ...Option,
) (*Service, error) {
	if validation.IsNilInterface(sessionStore) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "sessionlogout.NewService: sessionStore must not be nil")
	}
	if validation.IsNilInterface(refreshStore) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "sessionlogout.NewService: refreshStore must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		sessionStore: sessionStore,
		refreshStore: refreshStore,
		emitter:      outbox.NewNoopEmitter(),
		logger:       logger,
	}
	for _, o := range opts {
		o(s)
	}
	if s.txRunner == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "sessionlogout: TxRunner required; use WithTxManager")
	}
	return s, nil
}

// persistRevoke wraps the session update + event emit in a transaction runner.
func (s *Service) persistRevoke(ctx context.Context, fn func(context.Context) error) error {
	return s.txRunner.RunInTx(ctx, fn)
}

// Logout revokes the caller's own session identified by sessionID. Cascades
// the revocation to the refresh-token chain so a stolen refresh token cannot
// survive logout.
//
// Ownership is enforced by fetching the session and comparing SubjectID
// against callerUserID. A session that does not exist OR does not belong to
// the caller yields the same ErrSessionNotFound — preventing cross-user
// session enumeration (IDOR). SubjectID is immutable post-create, so there
// is no TOCTOU window between the Get and the Revoke.
func (s *Service) Logout(ctx context.Context, sessionID, callerUserID string) error {
	if err := validation.RequireNotEmpty(errcode.ErrAuthLogoutInvalidInput,
		validation.F("id", sessionID),
	); err != nil {
		return err
	}
	if callerUserID == "" {
		// callerUserID is derived from JWT claims by the auth middleware, not from
		// client input. A blank value indicates a server-side auth misconfiguration,
		// not a missing request field — expose a generic message to the client.
		return errcode.New(errcode.KindInvalid, errcode.ErrAuthLogoutInvalidInput, "logout requires authenticated caller")
	}

	// Wrap the owner-scoped revoke + refresh cascade + outbox write in a transaction for L2 atomicity.
	revokeAndPublish := func(txCtx context.Context) error {
		sess, err := s.sessionStore.Get(txCtx, sessionID)
		if err != nil {
			if errcode.IsInfraError(err) {
				// Infra failures (PG outage, connection error) must surface as
				// 503 so clients retry instead of silently treating the session
				// as gone — squashing every Get error into not-found would
				// leak revocation status guarantees and mask real outages.
				s.logger.Error("session-logout: session lookup infra error",
					slog.Any("error", err), slog.String("session_id", sessionID))
				return errcode.Wrap(errcode.KindUnavailable, errcode.ErrAuthLogoutUnavailable,
					"session lookup unavailable", err)
			}
			// Domain not-found: unify with owner-mismatch into the same error
			// code to prevent cross-user session enumeration (IDOR).
			return errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "session not found")
		}
		if sess.SubjectID != callerUserID {
			return errcode.New(errcode.KindNotFound, errcode.ErrSessionNotFound, "session not found")
		}
		if err := s.sessionStore.Revoke(txCtx, sessionID); err != nil {
			return err
		}
		if err := s.refreshStore.RevokeSession(txCtx, sessionID); err != nil {
			return fmt.Errorf("session-logout: revoke refresh chain: %w", err)
		}
		return outbox.Emit(txCtx, s.emitter, dto.TopicSessionRevoked, dto.SessionRevokedEvent{
			SessionID: sessionID,
			UserID:    callerUserID,
		})
	}

	if err := s.persistRevoke(ctx, revokeAndPublish); err != nil {
		return err
	}

	s.logger.Info("session revoked",
		slog.String("session_id", sessionID), slog.String("user_id", callerUserID))
	return nil
}
