// Package sessionlogout implements the session-logout slice: revokes sessions
// and publishes revocation events.
package sessionlogout

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// Option configures a session-logout Service.
type Option func(*Service)

// WithEmitter sets the event emitter. Accepts a sealed outbox.CellEmitter;
// typed-nil inputs are silently ignored (builder-option semantics).
func WithEmitter(e outbox.CellEmitter) Option {
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
	sessionStore session.Store             `gocell:"required"`
	refreshStore refresh.Store             `gocell:"required"`
	txRunner     persistence.CellTxManager `gocell:"required" gocellErr:"sessionlogout: TxRunner required; use WithTxManager"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	clk          clock.Clock               `gocell:"required" gocellErr:"sessionlogout.NewService: clock.Clock required"`      //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	emitter      outbox.CellEmitter
	logger       *slog.Logger
}

// NewService creates a session-logout Service. refreshStore is required so
// that logout also revokes the refresh-token chain for the session — without
// this, a stolen refresh token would survive logout.
func NewService(
	clk clock.Clock,
	sessionStore session.Store,
	refreshStore refresh.Store,
	logger *slog.Logger,
	opts ...Option,
) (*Service, error) {
	clock.MustHaveClock(clk, "sessionlogout.NewService")
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		sessionStore: sessionStore,
		refreshStore: refreshStore,
		clk:          clk,
		emitter:      outbox.DemoCellEmitter(),
		logger:       logger,
	}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
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
	if err := validation.RequireNotEmpty(
		errcode.ErrAuthLogoutInvalidInput,
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
	if err := s.persistRevoke(ctx, func(txCtx context.Context) error {
		return s.revokeAndPublish(txCtx, sessionID, callerUserID)
	}); err != nil {
		return err
	}

	s.logger.Info("session revoked",
		slog.String("session_id", sessionID), slog.String("user_id", callerUserID))
	return nil
}

// sessionSubjectID is the nil-safe ownerID accessor used by auth.CheckOwner.
// Returning "" for nil sess collapses lookup-failure into the same KindNotFound
// envelope as owner-mismatch (IDOR-safe 404 collapse).
func sessionSubjectID(v *session.ValidateView) string {
	if v == nil {
		return ""
	}
	return v.SubjectID
}

// revokeAndPublish runs the L2 owner-scoped revoke + refresh cascade + outbox
// write inside a transaction. Split from Logout to keep Logout under the
// cognitive complexity budget and to give the transactional body a name in
// stack traces.
func (s *Service) revokeAndPublish(txCtx context.Context, sessionID, callerUserID string) error {
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
		// Domain error (typically KindNotFound from store implementations).
		// Defensively null sess regardless of what the Store returned: the
		// Store.Get contract is (nil, err) on failure, but explicit nilling
		// guarantees CheckOwner's nil-safe accessor path is taken even if
		// a future implementation deviates. Any new non-infra domain error
		// class is funneled into KindNotFound here — currently safe because
		// only KindNotFound is expected; revisit if Store.Get adds other
		// domain error classes.
		sess = nil
	}
	// Domain not-found and owner mismatch are unified into the same envelope
	// via auth.CheckOwner (IDOR-safe 404 collapse): for not-found, sess is
	// nil and sessionSubjectID returns "", which fails != against the
	// non-empty callerUserID (pre-validated at the empty-callerUserID guard
	// at the top of Logout, raising KindInvalid; CheckOwner also fail-closes
	// on empty callerID as defense-in-depth).
	if err := auth.CheckOwner(sess, sessionSubjectID, callerUserID,
		errcode.ErrSessionNotFound); err != nil {
		return err
	}
	if err := s.sessionStore.Revoke(txCtx, sessionID); err != nil {
		return err
	}
	if err := s.refreshStore.RevokeSession(txCtx, sessionID); err != nil {
		return fmt.Errorf("session-logout: revoke refresh chain: %w", err)
	}
	return outbox.Emit(txCtx, s.clk, s.emitter, dto.TopicSessionRevoked, dto.SessionRevokedEvent{
		SessionID: sessionID,
		UserID:    callerUserID,
	})
}
