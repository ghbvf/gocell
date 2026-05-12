// Package sessionlogout implements the session-logout slice: revokes sessions
// and publishes revocation events.
package sessionlogout

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
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
	sessionRepo  ports.SessionRepository
	refreshStore refresh.Store
	txRunner     persistence.CellTxManager
	emitter      outbox.Emitter
	logger       *slog.Logger
}

// NewService creates a session-logout Service. refreshStore is required so
// that logout also revokes the refresh-token chain for the session — without
// this, a stolen refresh token would survive logout.
func NewService(
	sessionRepo ports.SessionRepository,
	refreshStore refresh.Store,
	logger *slog.Logger,
	opts ...Option,
) (*Service, error) {
	if validation.IsNilInterface(sessionRepo) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "sessionlogout.NewService: sessionRepo must not be nil")
	}
	if validation.IsNilInterface(refreshStore) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "sessionlogout.NewService: refreshStore must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		sessionRepo:  sessionRepo,
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

// MustNewService is the static-wiring variant of NewService.
func MustNewService(
	sessionRepo ports.SessionRepository,
	refreshStore refresh.Store,
	logger *slog.Logger,
	opts ...Option,
) *Service {
	s, err := NewService(sessionRepo, refreshStore, logger, opts...)
	if err != nil {
		panic(panicregister.Approved("sessionlogout-invariant", errcode.Assertion("sessionlogout: invariant violated: %v", err)))
	}
	return s
}

// persistRevoke wraps the session update + event emit in a transaction runner.
func (s *Service) persistRevoke(ctx context.Context, fn func(context.Context) error) error {
	return s.txRunner.RunInTx(ctx, fn)
}

// Logout revokes the caller's own session identified by sessionID. Cascades
// the revocation to the refresh-token chain so a stolen refresh token cannot
// survive logout.
//
// Ownership is enforced inside the repository query (RevokeByIDAndOwner)
// rather than a handler-side compare, eliminating the TOCTOU window and
// preventing cross-user session enumeration (IDOR). A session that does not
// exist OR does not belong to the caller yields the same ErrSessionNotFound.
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
		if err := s.sessionRepo.RevokeByIDAndOwner(txCtx, sessionID, callerUserID); err != nil {
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

// LogoutUser revokes all sessions AND the refresh-token chains for the user.
// Both operations are wrapped in a transaction (F2) so a partial failure does
// not leave refresh chains live while sessions are revoked or vice-versa.
func (s *Service) LogoutUser(ctx context.Context, userID string) error {
	if userID == "" {
		return errcode.New(errcode.KindInvalid, errcode.ErrAuthLogoutInvalidInput, "logout requires a valid user identifier")
	}

	if err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		if err := s.sessionRepo.RevokeByUserID(txCtx, userID); err != nil {
			return fmt.Errorf("session-logout: revoke all: %w", err)
		}
		if err := s.refreshStore.RevokeUser(txCtx, userID); err != nil {
			return fmt.Errorf("session-logout: revoke refresh chains: %w", err)
		}
		return nil
	}); err != nil {
		return err
	}

	s.logger.Info("all sessions revoked for user", slog.String("user_id", userID))
	return nil
}
