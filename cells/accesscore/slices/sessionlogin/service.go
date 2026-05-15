// Package sessionlogin implements the session-login slice: password-based login
// with JWT access token and opaque refresh token issuance.
package sessionlogin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
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

// errMsgInvalidCredentials is the single public login error message used for
// all credential-failure paths (missing user, bad password, inactive account,
// epoch-version race). Using one const prevents account-status enumeration
// via differing error messages on the unauthenticated public login endpoint.
// ref: sessionvalidate const errMsgAuthFailed; sessionrefresh const errMsgInvalidRefreshToken.
const errMsgInvalidCredentials = "invalid credentials"

// passwordComparer is the signature of bcrypt.CompareHashAndPassword. It is
// a field on Service so tests can inject a spy without importing bcrypt directly.
// Production code uses bcrypt.CompareHashAndPassword (injected in NewService default).
type passwordComparer func(hash, password []byte) error

// dummyBcryptHash is a pre-computed bcrypt hash used when the user is not found.
// Comparing against it normalises timing so callers cannot distinguish "user not
// found" from "wrong password" via response latency.
//
// The hash is generated at domain.BcryptCost (=12) — identical to the cost used
// for real user passwords. Using a lower cost (e.g. bcrypt.MinCost=4) would make
// the "user not found" path ~256x faster than the "wrong password" path, exposing
// a statistical timing oracle that can enumerate valid usernames.
var dummyBcryptHash = func() []byte {
	h, err := bcrypt.GenerateFromPassword([]byte("dummy-timing-normalization"), domain.BcryptCost)
	if err != nil {
		panic(panicregister.Approved("sessionlogin-dummy-hash-init",
			errcode.Assertion("sessionlogin: failed to pre-compute dummyBcryptHash: %v", err)))
	}
	return h
}()

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

// withPasswordComparer overrides the bcrypt comparator used by Login. This
// option is package-private (lowercase) and intended only for unit tests that
// need to spy on or stub the password comparison step.
func withPasswordComparer(fn passwordComparer) Option {
	return func(s *Service) {
		if fn != nil {
			s.comparePassword = fn
		}
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
	userRepo        ports.UserRepository
	sessionStore    session.Store
	roleRepo        ports.RoleRepository
	refreshStore    refresh.Store
	txRunner        persistence.CellTxManager
	emitter         outbox.Emitter
	issuer          *auth.JWTIssuer
	logger          *slog.Logger
	clock           clock.Clock
	sessionTTL      time.Duration
	comparePassword passwordComparer // defaults to bcrypt.CompareHashAndPassword
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
		userRepo:        userRepo,
		sessionStore:    sessionStore,
		roleRepo:        roleRepo,
		refreshStore:    refreshStore,
		emitter:         outbox.NewNoopEmitter(),
		issuer:          issuer,
		logger:          logger,
		comparePassword: bcrypt.CompareHashAndPassword,
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
//
// S4d P1-#3 fix: credential validation + session/refresh INSERT run inside
// RunInTx with a SELECT ... FOR UPDATE on the users row. This serializes Login
// against credentialinvalidate.Invalidator.Apply (which also acquires a
// FOR UPDATE lock via BumpAuthzEpoch): a concurrent revoke cannot advance
// users.authz_epoch between the snapshot read and the downstream INSERTs,
// so session.AuthzEpochAtIssue is guaranteed to match the epoch that was
// valid at the moment of INSERT.
//
// S4d P1.1 password-version-pin invariant: preVersion is captured from the
// pre-bcrypt snapshot. The FOR UPDATE re-fetch inside loginInTx checks that
// the locked row's PasswordVersion still matches preVersion. A concurrent
// ChangePassword committing in the race window bumps PasswordVersion; this
// mismatch causes loginInTx to return ErrAuthLoginFailed, closing the
// old-password-mints-new-epoch-session race.
func (s *Service) Login(ctx context.Context, input LoginInput) (dto.TokenPair, error) {
	if err := validation.RequireNotEmpty(errcode.ErrAuthLoginInvalidInput,
		validation.F("username", input.Username),
		validation.F("password", input.Password),
	); err != nil {
		return dto.TokenPair{}, err
	}

	// Authenticate the password outside the tx (bcrypt is CPU-bound and must
	// not hold a DB transaction open during the hash comparison). We re-fetch
	// the user inside the tx with FOR UPDATE to get the authoritative epoch.
	//
	// C1: All credential-failure paths (missing user, wrong password, inactive
	// account) return the SAME error (ErrAuthLoginFailed / KindUnauthenticated /
	// errMsgInvalidCredentials). This prevents account-existence enumeration and
	// account-status enumeration via differing status codes or messages.
	//
	// Timing normalisation: bcrypt runs for every attempt regardless of whether
	// the user exists or is active, so callers cannot distinguish "user not found"
	// from "wrong password" via response latency (zitadel-style constant-time path).
	preUser, userLookupErr := s.userRepo.GetByUsername(ctx, input.Username)

	// Choose the hash to compare against. If the user does not exist we use
	// dummyBcryptHash to maintain constant time; if found we use the real hash.
	hashToCompare := dummyBcryptHash
	if userLookupErr == nil {
		hashToCompare = []byte(preUser.PasswordHash)
	}

	// Always run bcrypt — this is the constant-time anchor that prevents timing
	// sidechannels regardless of user-lookup outcome or account status.
	bcryptErr := s.comparePassword(hashToCompare, []byte(input.Password))

	// Evaluate the unified failure condition. Internal reasons are logged via
	// WithInternal only; they never appear in the 4xx response body.
	switch {
	case userLookupErr != nil:
		return dto.TokenPair{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthLoginFailed,
			errMsgInvalidCredentials,
			errcode.WithInternal(fmt.Sprintf("user lookup failed: %v", userLookupErr)))
	case !preUser.CanAuthenticate():
		// C1: inactive account → same 401 as bad password. Real reason in WithInternal.
		// R4: log only preUser.ID and preUser.Status() — NOT the full struct which
		// contains PasswordHash. Using %v on *domain.User would leak the hash into
		// slog/trace via errcode Internal (PR #501 RC-E, R4 fix).
		return dto.TokenPair{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthLoginFailed,
			errMsgInvalidCredentials,
			errcode.WithInternal(fmt.Sprintf("account not active (user_id=%s status=%v bcrypt_ok=%v)",
				preUser.ID, preUser.Status(), bcryptErr == nil)))
	case bcryptErr != nil:
		return dto.TokenPair{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthLoginFailed,
			errMsgInvalidCredentials)
	}

	// Pin the PasswordVersion from the pre-bcrypt snapshot. loginInTx will
	// re-check this against the FOR UPDATE locked row to detect a concurrent
	// ChangePassword committed in the race window (P1.1).
	preVersion := preUser.PasswordVersion

	// Re-fetch inside tx with FOR UPDATE to pin authz_epoch atomically.
	// If the user was deactivated between the pre-check and the tx, the
	// CanAuthenticate guard inside loginInTx rejects it — no silent
	// credential issuance. The transactional body is extracted to keep
	// Login's cognitive complexity within the CLAUDE.md ≤15 budget after
	// S4d added the FOR UPDATE re-fetch + post-check.
	sessionID := uuid.NewString()
	var pair dto.TokenPair
	if err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		p, err := s.loginInTx(txCtx, input.Username, sessionID, preVersion)
		if err != nil {
			return err
		}
		pair = p
		return nil
	}); err != nil {
		return dto.TokenPair{}, err
	}

	s.logger.Info("user logged in",
		slog.String("user_id", pair.UserID), slog.String("session_id", sessionID))
	return pair, nil
}

// loginInTx is the FOR-UPDATE-locked body of Login. It re-fetches the user
// inside the ambient transaction (acquiring the user-row write lock), checks
// CanAuthenticate, mints the access token, creates the session row, issues
// the refresh chain root, and emits the session.created outbox entry — all
// while holding the row lock so concurrent Invalidator.Apply cannot advance
// users.authz_epoch between the snapshot read and the session/refresh
// INSERTs (S4d §D2; PR #490 review P1-#3 fix).
//
// preVersion is the PasswordVersion captured from the pre-bcrypt snapshot.
// If the locked row's PasswordVersion differs, a concurrent ChangePassword
// committed in the race window — the old password must be rejected (P1.1).
//
// R3 error classification: only credential-domain errors (user not found:
// KindNotFound) are collapsed into the opaque 401 ErrAuthLoginFailed.
// Infrastructure errors (KindInternal, KindUnavailable, etc.) are passed
// through as-is to preserve their HTTP status (5xx / 503), preventing
// infra faults from being silently disguised as authentication failures.
func (s *Service) loginInTx(txCtx context.Context, username, sessionID string, preVersion int64) (dto.TokenPair, error) {
	user, err := s.userRepo.GetByUsernameForUpdate(txCtx, username)
	if err != nil {
		return dto.TokenPair{}, classifyForUpdateErr(err)
	}
	// C1: concurrent deactivation race — account was active at pre-bcrypt check but
	// was locked/suspended before the FOR UPDATE lock. Return the same 401 error as
	// other failure paths to prevent account-status enumeration. Real reason in logs.
	if !user.CanAuthenticate() {
		return dto.TokenPair{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthLoginFailed,
			errMsgInvalidCredentials,
			errcode.WithInternal(fmt.Sprintf("account deactivated in race window (in-tx check): user=%s", username)))
	}
	// P1.1: reject if a concurrent ChangePassword committed between the pre-bcrypt
	// snapshot and this FOR UPDATE lock. Uniform message prevents version enumeration.
	if user.PasswordVersion != preVersion {
		return dto.TokenPair{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthLoginFailed,
			errMsgInvalidCredentials)
	}

	minted, err := sessionmint.MintAccess(txCtx, sessionmint.Deps{
		Issuer:   s.issuer,
		RoleRepo: s.roleRepo,
		Clk:      s.clock,
	}, sessionmint.Request{
		UserID:                user.ID,
		SessionID:             sessionID,
		PasswordResetRequired: user.PasswordResetRequired(),
	})
	if err != nil {
		s.logger.Error("session-login: token issuance failed",
			slog.Any("error", err), slog.String("user_id", user.ID))
		return dto.TokenPair{}, err
	}

	now := s.clock.Now()
	sess := &session.Session{
		ID:        sessionID,
		SubjectID: user.ID,
		// session.JTI persists the original login-time JWT jti claim per
		// RFC 9068 §2.2.4. Refresh keeps session.ID stable but mints fresh
		// jti per access token; the row stores the first one as the
		// FingerprintJTIRef anchor (session.go godoc).
		JTI: minted.JTI,
		// AuthzEpochAtIssue is snapshotted while holding the FOR UPDATE
		// row lock — concurrent Invalidator.Apply cannot advance the epoch
		// between this read and the session INSERT (S4d §D2).
		AuthzEpochAtIssue: user.AuthzEpoch(),
		CreatedAt:         now,
		ExpiresAt:         now.Add(s.sessionTTL),
	}

	if err := s.sessionStore.Create(txCtx, sess); err != nil {
		return dto.TokenPair{}, fmt.Errorf("session-login: persist session: %w", err)
	}
	refreshWire, _, err := s.refreshStore.Issue(txCtx, sess.ID, user.ID, user.AuthzEpoch())
	if err != nil {
		s.logger.Error("session-login: refresh store issue failed",
			slog.Any("error", err), slog.String("user_id", user.ID))
		if isNoopTx(s.txRunner) {
			_ = s.sessionStore.Revoke(context.WithoutCancel(txCtx), sess.ID)
		}
		return dto.TokenPair{}, errcode.Wrap(errcode.KindUnavailable, errcode.ErrAuthRefreshUnavailable, "refresh store unavailable", err)
	}
	if err := outbox.Emit(txCtx, s.emitter, dto.TopicSessionCreated, dto.SessionCreatedEvent{
		SessionID: sess.ID,
		UserID:    user.ID,
	}); err != nil {
		if isNoopTx(s.txRunner) {
			s.cleanupIssuedSession(txCtx, sess.ID)
		}
		return dto.TokenPair{}, fmt.Errorf("session-login: emit event: %w", err)
	}
	return dto.TokenPair{
		AccessToken:           minted.AccessToken,
		RefreshToken:          refreshWire,
		ExpiresAt:             minted.ExpiresAt,
		SessionID:             sessionID,
		UserID:                user.ID,
		PasswordResetRequired: user.PasswordResetRequired(),
	}, nil
}

// persistSessionWithRefresh writes the session, issues the refresh root, and
// emits the session.created outbox entry inside the same transaction boundary
// when a durable TxRunner is configured. In demo mode it compensates the
// already-created session if refresh issuance fails.
//
// Always emits event.session.created.v1 — IssueForUser must record session
// creation for the audit trail. Login uses this path only via IssueForUser;
// the Login method itself manages its own RunInTx with FOR UPDATE.
//
// authzEpoch must be the epoch already stored on sess.AuthzEpochAtIssue;
// it is passed explicitly so the refresh.Issue call uses the same value
// without re-reading the sess field (avoids silent zero if caller forgets
// to set AuthzEpochAtIssue).
func (s *Service) persistSessionWithRefresh(ctx context.Context, sess *session.Session, userID string, authzEpoch int64) (string, error) {
	var refreshWire string
	do := func(txCtx context.Context) error {
		if err := s.sessionStore.Create(txCtx, sess); err != nil {
			return fmt.Errorf("session-login: persist session: %w", err)
		}
		wire, _, err := s.refreshStore.Issue(txCtx, sess.ID, userID, authzEpoch)
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

// classifyForUpdateErr maps errors from GetByUsernameForUpdate / GetByIDForUpdate
// to the appropriate caller-facing error:
//
//   - KindNotFound (user row absent) → opaque 401 ErrAuthLoginFailed.
//     The user was found in the pre-bcrypt read but disappeared before the
//     FOR UPDATE re-fetch — treat as a credential failure to prevent
//     enumeration.
//   - Everything else (KindInternal, KindUnavailable, infra errors) → pass
//     through as-is. Infra failures must NOT be disguised as 401; callers
//     must see the true 5xx / 503 so on-call can distinguish a transient
//     infra outage from a credential attack (R3 fix, PR #501 RC-E).
func classifyForUpdateErr(err error) error {
	var ec *errcode.Error
	if errors.As(err, &ec) && ec.Kind == errcode.KindNotFound {
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthLoginFailed,
			errMsgInvalidCredentials)
	}
	return err
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
// F18 — GetByID not GetByIDForUpdate (cross-slice; FOR UPDATE would span slices):
// IssueForUser uses a plain GetByID (no SELECT FOR UPDATE) for the user fetch.
// Using FOR UPDATE here would escalate a row-level lock across a cross-slice
// boundary (sessionlogin ↔ identitymanage), coupling their transaction scopes
// in a way that violates the Cell isolation model. The read is cross-slice by
// contract (identitymanage calls IssueForUser via the TokenIssuer interface).
//
// Concurrency trade-off (KNOWN, ACCEPTED): a concurrent Lock/BumpAuthzEpoch
// between the GetByID read and the session-persist write could issue a session
// with the pre-bump epoch (stale AuthzEpochAtIssue). This window is defended
// by two layers:
//
//  1. sessionvalidate epoch-mismatch check: the newly-issued stale-epoch
//     session is rejected at first validate (session row epoch < user epoch),
//     making the token useless before it can cause harm.
//  2. P1.3b CanAuthenticate check (defense-in-depth, added this PR):
//     sessionvalidate.enforceSessionState fail-closes any request from a
//     non-active user regardless of epoch.
//
// The stale-epoch defense is covered by sessionvalidate unit tests (not
// duplicated here — an integration test would require testcontainers unavailable
// in sandbox). The window is documented as accepted.
//
// IMPORTANT (PR-CFG-G1): IssueForUser ALWAYS emits event.session.created.v1
// — every successful call produces a session event with the new session ID.
// Callers that do not want a session-creation event must avoid this method.
// Refresh-token rotation (sessionrefresh.Refresh) does NOT call IssueForUser;
// it reuses the existing session record and updates only AccessToken/ExpiresAt,
// so refresh flows do not double-emit.
//
// P1.3a active-gate: IssueForUser fail-closes for non-active users (suspended,
// locked), consistent with Login and sessionrefresh. A non-active user must not
// receive a fresh token pair even via the ChangePassword path.
//
// Returns dto.TokenPair (internal/dto, value not pointer) so this method
// implements the identitymanage.TokenIssuer interface without a cross-slice
// import (F-ARCH-1). Value type makes (nil, nil) unrepresentable.
func (s *Service) IssueForUser(ctx context.Context, userID string) (dto.TokenPair, error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return dto.TokenPair{}, fmt.Errorf("session-login: IssueForUser get user: %w", err)
	}
	if !user.CanAuthenticate() {
		return dto.TokenPair{}, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthUserNotActive,
			"account is not active")
	}

	sessionID := uuid.NewString()
	minted, err := sessionmint.MintAccess(ctx, sessionmint.Deps{
		Issuer:   s.issuer,
		RoleRepo: s.roleRepo,
		Clk:      s.clock,
	}, sessionmint.Request{
		UserID:                userID,
		SessionID:             sessionID,
		PasswordResetRequired: user.PasswordResetRequired(),
	})
	if err != nil {
		s.logger.Error("session-login: IssueForUser token issuance failed",
			slog.Any("error", err), slog.String("user_id", userID))
		return dto.TokenPair{}, err
	}

	// Persist the session so sessionvalidate can look it up by sid claim.
	// session.JTI carries the original JWT jti claim (RFC 9068 §2.2.4) — see
	// matching note in the login path above.
	// AuthzEpochAtIssue is snapshotted from user.AuthzEpoch at IssueForUser
	// call time. IssueForUser is only called after ChangePassword (which bumps
	// the epoch inside its own tx), so the epoch is already advanced before
	// we reach here — row-provenance invariant (S4d §A8) is maintained.
	now := s.clock.Now()
	sess := &session.Session{
		ID:                sessionID,
		SubjectID:         userID,
		JTI:               minted.JTI,
		AuthzEpochAtIssue: user.AuthzEpoch(),
		CreatedAt:         now,
		ExpiresAt:         now.Add(s.sessionTTL),
	}
	refreshWire, err := s.persistSessionWithRefresh(ctx, sess, userID, user.AuthzEpoch())
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
		PasswordResetRequired: user.PasswordResetRequired(),
	}, nil
}
