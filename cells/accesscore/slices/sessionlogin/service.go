// Package sessionlogin implements the session-login slice: password-based login
// with JWT access token and opaque refresh token issuance.
package sessionlogin

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/cells/accesscore/internal/sessionmint"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/panicregister"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	session "github.com/ghbvf/gocell/runtime/auth/session"
)

// Option configures a session-login Service.
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

// WithClock sets the clock used for session creation timestamps.
// clk must not be nil; pass clock.Real() for production use.
func WithClock(clk clock.Clock) Option {
	return func(s *Service) {
		clock.MustHaveClock(clk, "sessionlogin.WithClock")
		s.clock = clk
	}
}

// WithSessionTTL sets the session row's GC-eligibility lifetime. Session
// rows should outlive the refresh chain so that revocation lookups remain
// effective for the chain's entire lifetime; composition roots typically
// inject accesscore.DefaultRefreshMaxAge here.
//
// This is NOT the access-token TTL (which is the JWT's exp claim, set by
// sessionmint) and NOT a validate-time gate — Session.ExpiresAt is
// projected out of Store.Get's *ValidateView return type so validate
// paths cannot reach it.
func WithSessionTTL(d time.Duration) Option {
	return func(s *Service) {
		if d > 0 {
			s.sessionTTL = d
		}
	}
}

// Service implements password login with JWT issuance.
type Service struct {
	userRepo     ports.UserRepository
	sessionStore session.Store
	roleRepo     ports.RoleRepository
	refreshStore refresh.Store
	txRunner     persistence.CellTxManager
	emitter      outbox.Emitter
	issuer       *auth.JWTIssuer
	logger       *slog.Logger
	clock        clock.Clock
	sessionTTL   time.Duration
}

// NewService creates a session-login Service. refreshStore issues the opaque
// refresh token returned to the client; the access JWT is minted by
// sessionmint.MintAccess.
func NewService(
	userRepo ports.UserRepository,
	sessionStore session.Store,
	roleRepo ports.RoleRepository,
	refreshStore refresh.Store,
	issuer *auth.JWTIssuer,
	logger *slog.Logger,
	opts ...Option,
) (*Service, error) {
	if validation.IsNilInterface(userRepo) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "sessionlogin.NewService: userRepo must not be nil")
	}
	if validation.IsNilInterface(sessionStore) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "sessionlogin.NewService: sessionStore must not be nil")
	}
	if validation.IsNilInterface(roleRepo) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "sessionlogin.NewService: roleRepo must not be nil")
	}
	if validation.IsNilInterface(refreshStore) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "sessionlogin.NewService: refreshStore must not be nil")
	}
	if issuer == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "sessionlogin.NewService: issuer must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		userRepo:     userRepo,
		sessionStore: sessionStore,
		roleRepo:     roleRepo,
		refreshStore: refreshStore,
		emitter:      outbox.NewNoopEmitter(),
		issuer:       issuer,
		logger:       logger,
	}
	for _, o := range opts {
		o(s)
	}
	if s.txRunner == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "sessionlogin: TxRunner required; use WithTxManager")
	}
	clock.MustHaveClock(s.clock, "sessionlogin.NewService: clock required — use WithClock(c.clk)")
	if s.sessionTTL <= 0 {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"sessionlogin: SessionTTL required; use WithSessionTTL (typically accesscore.DefaultRefreshMaxAge)")
	}
	return s, nil
}

// MustNewService is the static-wiring variant of NewService.
func MustNewService(
	userRepo ports.UserRepository,
	sessionStore session.Store,
	roleRepo ports.RoleRepository,
	refreshStore refresh.Store,
	issuer *auth.JWTIssuer,
	logger *slog.Logger,
	opts ...Option,
) *Service {
	s, err := NewService(userRepo, sessionStore, roleRepo, refreshStore, issuer, logger, opts...)
	if err != nil {
		panic(panicregister.Approved("sessionlogin-invariant", errcode.Assertion("sessionlogin: invariant violated: %v", err)))
	}
	return s
}

// LoginInput holds login parameters.
type LoginInput struct {
	Username string
	Password string
}

// Login authenticates a user and returns a JWT token pair.
func (s *Service) Login(ctx context.Context, input LoginInput) (dto.TokenPair, error) {
	if err := validation.RequireNotEmpty(errcode.ErrAuthLoginInvalidInput,
		validation.F("username", input.Username),
		validation.F("password", input.Password),
	); err != nil {
		return dto.TokenPair{}, err
	}

	user, err := s.userRepo.GetByUsername(ctx, input.Username)
	if err != nil {
		return dto.TokenPair{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthLoginFailed, "invalid credentials")
	}

	if !user.CanAuthenticate() {
		// S4.0: fail-closed for any non-active user. Pre-S4.0 only blocked
		// 'locked'; a 'suspended' user could log in, defeating an admin's
		// suspend gesture. domain.User.CanAuthenticate() is the single source
		// of truth for "is this user allowed to obtain or continue a session".
		return dto.TokenPair{}, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthUserNotActive,
			"account is not active")
	}

	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(input.Password)); err != nil {
		return dto.TokenPair{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthLoginFailed, "invalid credentials")
	}

	sessionID := uuid.NewString()
	minted, err := sessionmint.MintAccess(ctx, sessionmint.Deps{
		Issuer:   s.issuer,
		RoleRepo: s.roleRepo,
		Clk:      s.clock,
	}, sessionmint.Request{
		UserID:                user.ID,
		SessionID:             sessionID,
		PasswordResetRequired: user.PasswordResetRequired,
	})
	if err != nil {
		s.logger.Error("session-login: token issuance failed",
			slog.Any("error", err), slog.String("user_id", user.ID))
		return dto.TokenPair{}, err
	}

	now := s.clock.Now()
	sess := &session.Session{
		ID:                sessionID,
		SubjectID:         user.ID,
		JTI:               sessionID,
		AuthzEpochAtIssue: 0,
		CreatedAt:         now,
		ExpiresAt:         now.Add(s.sessionTTL),
	}

	refreshWire, err := s.persistSessionWithRefresh(ctx, sess, user.ID)
	if err != nil {
		return dto.TokenPair{}, err
	}

	s.logger.Info("user logged in",
		slog.String("user_id", user.ID), slog.String("session_id", sess.ID))
	return dto.TokenPair{
		AccessToken:           minted.AccessToken,
		RefreshToken:          refreshWire,
		ExpiresAt:             minted.ExpiresAt,
		SessionID:             sessionID,
		UserID:                user.ID,
		PasswordResetRequired: user.PasswordResetRequired,
	}, nil
}

// persistSessionWithRefresh writes the session, issues the refresh root, and
// emits the session.created outbox entry inside the same transaction boundary
// when a durable TxRunner is configured. In demo mode it compensates the
// already-created session if refresh issuance fails.
//
// Always emits event.session.created.v1 — both Login and IssueForUser paths
// must record session creation for audit trail (no emitCreated flag: removed
// per PR-CFG-G1 commit 2).
func (s *Service) persistSessionWithRefresh(ctx context.Context, sess *session.Session, userID string) (string, error) {
	var refreshWire string
	do := func(txCtx context.Context) error {
		if err := s.sessionStore.Create(txCtx, sess); err != nil {
			return fmt.Errorf("session-login: persist session: %w", err)
		}
		wire, _, err := s.refreshStore.Issue(txCtx, sess.ID, userID)
		if err != nil {
			s.logger.Error("session-login: refresh store issue failed",
				slog.Any("error", err), slog.String("user_id", userID))
			// In demo/noop-tx mode, the session was already written without a real
			// transaction; compensate explicitly. In durable-tx mode, the tx rollback
			// handles atomicity — no explicit cleanup is needed (and would double-revoke).
			if isNoopTx(s.txRunner) {
				_ = s.sessionStore.Revoke(context.WithoutCancel(txCtx), sess.ID)
			}
			return errcode.Wrap(errcode.KindUnavailable, errcode.ErrAuthRefreshUnavailable, "refresh store unavailable", err)
		}
		refreshWire = wire
		if err := outbox.Emit(txCtx, s.emitter, dto.TopicSessionCreated, dto.SessionCreatedEvent{
			SessionID: sess.ID,
			UserID:    userID,
		}); err != nil {
			// Same pattern: explicit cleanup only in noop/demo mode.
			if isNoopTx(s.txRunner) {
				s.cleanupIssuedSession(txCtx, sess.ID)
			}
			return fmt.Errorf("session-login: emit event: %w", err)
		}
		return nil
	}
	if err := s.txRunner.RunInTx(ctx, do); err != nil {
		return "", err
	}
	return refreshWire, nil
}

// isNoopTx reports whether r is a demo/noop TxRunner (implements cell.Nooper and
// returns Noop()==true). Used to decide whether explicit session cleanup is
// needed on failure paths: noop tx has no rollback, so we compensate manually;
// durable tx rollback handles atomicity.
func isNoopTx(r persistence.TxRunner) bool {
	n, ok := r.(cell.Nooper)
	return ok && n.Noop()
}

func (s *Service) cleanupIssuedSession(ctx context.Context, sessionID string) {
	cleanupCtx := context.WithoutCancel(ctx)
	if err := s.refreshStore.RevokeSessionDetached(ctx, sessionID); err != nil {
		s.logger.Error("session-login: cleanup refresh chain failed",
			slog.Any("error", err), slog.String("session_id", sessionID))
	}
	// session.Store.Revoke is idempotent: missing IDs are no-ops returning nil
	// (防枚举 — append-only revoke semantics per ADR-Session D3).
	if err := s.sessionStore.Revoke(cleanupCtx, sessionID); err != nil {
		s.logger.Error("session-login: cleanup session revoke failed",
			slog.Any("error", err), slog.String("session_id", sessionID))
	}
}

// IssueForUser issues a fresh token pair for a user by ID. It re-fetches the
// user and their roles so the returned tokens reflect the current state (e.g.
// after ChangePassword clears PasswordResetRequired). Used by identitymanage
// ChangePassword to issue a replacement token pair without forcing a re-login.
//
// A new Session record is persisted to sessionRepo so that sessionvalidate can
// look up the session by its sid claim and enforce revocation/expiry. Without
// this step, sessionvalidate.enforceSessionState fails with "not found" → 401
// on the very next authenticated request (root cause of PR#183 round-2 CI failure).
//
// IMPORTANT (PR-CFG-G1): IssueForUser ALWAYS emits event.session.created.v1
// — every successful call produces a session event with the new session ID.
// Callers that do not want a session-creation event must avoid this method.
// Refresh-token rotation (sessionrefresh.Refresh) does NOT call IssueForUser;
// it reuses the existing session record and updates only AccessToken/ExpiresAt,
// so refresh flows do not double-emit.
//
// Returns dto.TokenPair (internal/dto, value not pointer) so this method
// implements the identitymanage.TokenIssuer interface without a cross-slice
// import (F-ARCH-1). Value type makes (nil, nil) unrepresentable.
func (s *Service) IssueForUser(ctx context.Context, userID string) (dto.TokenPair, error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return dto.TokenPair{}, fmt.Errorf("session-login: IssueForUser get user: %w", err)
	}

	sessionID := uuid.NewString()
	minted, err := sessionmint.MintAccess(ctx, sessionmint.Deps{
		Issuer:   s.issuer,
		RoleRepo: s.roleRepo,
		Clk:      s.clock,
	}, sessionmint.Request{
		UserID:                userID,
		SessionID:             sessionID,
		PasswordResetRequired: user.PasswordResetRequired,
	})
	if err != nil {
		s.logger.Error("session-login: IssueForUser token issuance failed",
			slog.Any("error", err), slog.String("user_id", userID))
		return dto.TokenPair{}, err
	}

	// Persist the session so sessionvalidate can look it up by sid claim.
	sess := &session.Session{
		ID:                sessionID,
		SubjectID:         userID,
		JTI:               sessionID, // same UUID used as store ID and JWT jti claim
		AuthzEpochAtIssue: 0,         // S4a placeholder; S4b will snapshot users.authz_epoch
		CreatedAt:         s.clock.Now(),
		ExpiresAt:         minted.ExpiresAt,
	}
	refreshWire, err := s.persistSessionWithRefresh(ctx, sess, userID)
	if err != nil {
		return dto.TokenPair{}, err
	}

	s.logger.Info("session-login: IssueForUser issued new session",
		slog.String("user_id", userID), slog.String("session_id", sessionID))

	return dto.TokenPair{
		AccessToken:           minted.AccessToken,
		RefreshToken:          refreshWire,
		ExpiresAt:             minted.ExpiresAt,
		SessionID:             sessionID,
		UserID:                userID,
		PasswordResetRequired: user.PasswordResetRequired,
	}, nil
}
