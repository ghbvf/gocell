// Package sessionlogin implements the session-login slice: password-based login
// with JWT access token and opaque refresh token issuance.
package sessionlogin

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/cells/accesscore/internal/accountlockout"
	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialauthority"
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
//
// The input is crypto/rand bytes, not a fixed literal: the dummy input value is
// irrelevant (it must only never equal a real password — random guarantees
// that) and a hardcoded string would be a meaningless known-plaintext that
// secret scanners flag. Nothing authenticates against this; there is no secret.
var dummyBcryptHash = func() []byte {
	seed := make([]byte, 32)
	if _, err := rand.Read(seed); err != nil {
		panic(panicregister.Approved("sessionlogin-dummy-hash-seed",
			errcode.Assertion("sessionlogin: failed to seed dummyBcryptHash: %v", err)))
	}
	h, err := bcrypt.GenerateFromPassword(seed, domain.BcryptCost)
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
	// lockout drives ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01: it owns the
	// failure-window policy, persists the counter, and routes lock/unlock
	// through authzmutate. Required (fail-fast in NewService) — sessionlogin
	// MUST NOT import authzmutate directly (depguard upstream Hard funnel
	// SESSIONLOGIN-LOCKOUT-VIA-ACCOUNTLOCKOUT-01).
	lockout *accountlockout.Service
}

// WithAccountLockout injects the auto-lockout mediator. Required for sessionlogin;
// the upstream depguard rule SESSIONLOGIN-LOCKOUT-VIA-ACCOUNTLOCKOUT-01 forces
// every lock decision through this funnel.
func WithAccountLockout(svc *accountlockout.Service) Option {
	return func(s *Service) {
		if svc != nil {
			s.lockout = svc
		}
	}
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
	if s.lockout == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"sessionlogin: AccountLockout required; use WithAccountLockout "+
				"(sessionlogin must route lock decisions through accountlockout, not authzmutate)")
	}
	return s, nil
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
//
// PR #585 review P1#1 fix: credential-failure paths (baseline assert fail,
// wrong password) do NOT return an error from the RunInTx closure. PG
// translates any non-nil return into ROLLBACK, which would silently drop the
// auto-lockout counter UPDATE that recordFailureBestEffort just wrote. The
// closure now returns nil on credential failures and signals the 401 via the
// outer-scope `failureErr` variable; the tx commits the counter increment
// (and, on threshold, the LockUser mutation + locked event), and Login
// returns the prepared 401 after RunInTx succeeds. Only infrastructure
// failures (DB / refresh-store / outbox emit) still return error from the
// closure to trigger a real ROLLBACK.
func (s *Service) Login(ctx context.Context, input LoginInput) (dto.TokenPair, error) {
	if err := validation.RequireNotEmpty(
		errcode.ErrAuthLoginInvalidInput,
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

	// Missing user → unified 401 without entering a tx. There is no user row to
	// lock or to increment failure counters against; the dummyBcryptHash above
	// already paid the timing cost so the latency profile matches the
	// wrong-password / inactive-account paths within their own bcrypt budget.
	if userLookupErr != nil {
		return dto.TokenPair{}, errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthLoginFailed,
			errMsgInvalidCredentials,
			errcode.WithInternal(fmt.Sprintf("user lookup failed: %v", userLookupErr)))
	}

	// User exists. Open a tx that holds the row lock across:
	//   1. TryLazyUnlock (locked + TTL elapsed → flip back to Active + reset counter)
	//   2. credentialauthority.Assert baseline (inactive / suspended → 401 + RecordFailure)
	//   3. bcrypt result check (wrong password → 401 + RecordFailure)
	//   4. session/refresh issue + outbox emit + RecordSuccess on the happy path
	//
	// pwVersionPin is the opaque WithPasswordVersionPin Check captured from the
	// pre-bcrypt snapshot via credentialauthority.SnapshotPasswordVersion so this
	// slice file never reads domain.User.PasswordVersion directly (Hard funnel
	// CREDENTIAL-AUTHORITY-ASSERT-FUNNEL-01 upstream prong).
	pwVersionPin := credentialauthority.SnapshotPasswordVersion(preUser)
	sessionID := uuid.NewString()
	var outcome loginOutcome
	if err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		o, infraErr := s.loginInTx(ctx, txCtx, input.Username, sessionID, pwVersionPin, bcryptErr)
		if infraErr != nil {
			return infraErr // real infra error → tx rollback
		}
		outcome = o
		// Returning nil even on credential failure is intentional: the
		// auto-lockout counter UPDATE (and any threshold-triggered LockUser
		// mutation) must commit. The 401 surfaces via outcome.failureErr
		// after RunInTx returns. PR #585 review P1#1.
		return nil
	}); err != nil {
		return dto.TokenPair{}, err
	}
	if outcome.failureErr != nil {
		return dto.TokenPair{}, outcome.failureErr
	}

	s.logger.Info("user logged in",
		slog.String("user_id", outcome.pair.UserID), slog.String("session_id", sessionID))
	return outcome.pair, nil
}

// loginOutcome carries the result of the in-tx login decision back to the
// outer Login wrapper. Combined with the second return of loginInTx (an
// infra error) it encodes three mutually-exclusive states:
//
//  1. Success — outcome.pair non-zero, outcome.failureErr nil, infra error nil.
//  2. Credential-domain rejection — outcome.pair zero, outcome.failureErr
//     non-nil (the 401), infra error nil. RunInTx must commit so the
//     auto-lockout counter UPDATE persists; the caller returns failureErr.
//  3. Infrastructure failure — outcome zero-value, infra error non-nil.
//     RunInTx will roll back; the caller propagates the infra error as 5xx.
type loginOutcome struct {
	pair       dto.TokenPair
	failureErr error // non-nil = credential-domain 401 to return after the tx commits
}

// loginInTx is the FOR-UPDATE-locked body of Login. It re-fetches the user
// inside the ambient transaction (acquiring the user-row write lock),
// invokes lazy-unlock if applicable, checks CanAuthenticate + password-version
// pin + bcrypt result, then on success mints the access token, creates the
// session row, issues the refresh chain root, and emits the session.created
// outbox entry — all while holding the row lock so concurrent Invalidator.Apply
// cannot advance users.authz_epoch between the snapshot read and the
// session/refresh INSERTs (S4d §D2; PR #490 review P1-#3 fix).
//
// pwVersionPin is the opaque WithPasswordVersionPin Check captured from the
// pre-bcrypt snapshot via credentialauthority.SnapshotPasswordVersion. If
// the locked row's PasswordVersion differs from the captured value, a
// concurrent ChangePassword committed in the race window — the old password
// must be rejected (P1.1).
//
// bcryptErr is the result of the pre-bcrypt password comparison performed
// outside the tx (timing-anchor). A non-nil value means the password did not
// match the user's stored hash; combined with a passing baseline assert this
// is the wrong-password failure path and increments the auto-lockout counter.
//
// R3 error classification: only credential-domain errors (user not found:
// KindNotFound) are collapsed into the opaque 401 ErrAuthLoginFailed.
// Infrastructure errors (KindInternal, KindUnavailable, etc.) are passed
// through as-is to preserve their HTTP status (5xx / 503), preventing
// infra faults from being silently disguised as authentication failures.
//
// multi-stage auto-lockout decision (lazy-unlock → baseline assert → bcrypt
// → mint+emit). Further extraction would scatter the row-lock invariant
// across helpers, breaking the "single tx, single locked row" contract that
// makes auto-lockout race-safe.
//
// loginInTx returns the in-tx login decision. The first return is the
// outcome — exactly one of outcome.pair / outcome.failureErr is populated
// on a non-infra completion (success vs credential-domain 401). The second
// return is reserved for infrastructure failures (DB / refresh-store / outbox);
// a non-nil error here causes the caller's RunInTx closure to return and
// PG to roll back the whole tx.
//
// PR #585 review P1#1: credential-domain failures (baseline assert / wrong
// password) are carried back via outcome.failureErr (not via the second
// error return) so the caller can return nil from the closure and let PG
// commit the auto-lockout counter UPDATE + threshold-triggered LockUser
// mutation. The previous signature returned the 401 as a real error from
// loginInTx, which the caller propagated to RunInTx → ROLLBACK, silently
// dropping the counter.
//
// multi-stage auto-lockout decision (lazy-unlock → baseline assert → bcrypt
// → mint+emit). Further extraction would scatter the row-lock invariant
// across helpers, breaking the "single tx, single locked row" contract that
// makes auto-lockout race-safe.
//
//nolint:gocognit,funlen // cognitive complexity + length are driven by the
func (s *Service) loginInTx(
	ctx context.Context,
	txCtx context.Context,
	username, sessionID string,
	pwVersionPin credentialauthority.Check,
	bcryptErr error,
) (loginOutcome, error) {
	user, err := s.userRepo.GetByUsernameForUpdate(txCtx, username)
	if err != nil {
		return loginOutcome{}, classifyForUpdateErr(err)
	}

	// Lazy-unlock: if the user is auto-locked and the TTL has elapsed,
	// transparently flip status back to Active and clear the counter. On
	// unlock we re-fetch the row inside the same tx so the rest of this
	// function operates on the post-mutation view (status=Active,
	// failed_login_count=0, locked_until=nil).
	unlocked, err := s.lockout.TryLazyUnlock(ctx, txCtx, user)
	if err != nil {
		return loginOutcome{}, fmt.Errorf("sessionlogin:lazy unlock: %w", err)
	}
	if unlocked {
		user, err = s.userRepo.GetByUsernameForUpdate(txCtx, username)
		if err != nil {
			return loginOutcome{}, classifyForUpdateErr(err)
		}
	}

	// Credentialauthority funnel: baseline (CanAuthenticate) re-check after the
	// FOR UPDATE lock closes the concurrent-deactivation race (C1), and
	// the password-version pin re-checks the version captured pre-bcrypt to
	// detect a concurrent ChangePassword that committed inside the race window
	// (P1.1). Both failure classes collapse to the uniform 401 with internal
	// reason; the caller may not branch on err to discover which check failed
	// (防枚举).
	//
	// Baseline + pwVersionPin failure also increments the auto-lockout counter
	// for Active users: concurrent-ChangePassword races are rare but
	// indistinguishable from a genuinely wrong password from the caller's
	// perspective; not counting them would create a small but real timing oracle.
	// For non-Active users accountlockout.RecordFailure short-circuits and does
	// not touch the counter (P1#2).
	if err := credentialauthority.Assert(user, pwVersionPin); err != nil {
		s.recordFailureBestEffort(ctx, txCtx, user, "baseline_assert")
		return loginOutcome{
			failureErr: errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthLoginFailed,
				errMsgInvalidCredentials,
				errcode.WithInternal(fmt.Sprintf("credentialauthority: in-tx assert failed (user_id=%s): %v", user.ID, err))),
		}, nil
	}

	// Wrong-password (post-baseline): increment auto-lockout counter. If the
	// new count >= threshold, accountlockout.RecordFailure runs the LockUser
	// mutation + emits event.user.locked.v1 inside this tx; subsequent reads
	// of the user row in the same tx see status=Locked. Return the unified
	// 401 either way.
	//
	// The closure-level nolint below is intentional: golangci-lint's nilerr
	// check would flag the `return ..., nil` because bcryptErr (observed on
	// the line just above) is dropped. That is exactly the contract this
	// branch implements — bcryptErr is a credential-domain rejection, not an
	// infra error; it is carried back via outcome.failureErr (NOT via the
	// second error return) so the caller can return nil from the closure
	// and let PG commit the auto-lockout counter UPDATE. Returning a real
	// error here would cause RunInTx → ROLLBACK and silently drop the
	// counter (PR #585 review P1#1). The nolint is placed on the `return`
	// line itself so golangci-lint's "must be on same line as offender"
	// scope rule applies precisely.
	if bcryptErr != nil {
		s.recordFailureBestEffort(ctx, txCtx, user, "wrong_password")
		return loginOutcome{ //nolint:nilerr // see godoc above; failureErr path
			failureErr: errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthLoginFailed,
				errMsgInvalidCredentials),
		}, nil
	}

	// Successful credentials. Reset the auto-lockout counter (no-op if it was
	// already clean) before minting tokens so the success path co-commits
	// counter clear + session/refresh INSERT + outbox emit.
	if err := s.lockout.RecordSuccess(txCtx, user); err != nil {
		s.logger.Error("sessionlogin:lockout reset failed",
			slog.Any("error", err), slog.String("user_id", user.ID))
		// Counter reset failure is non-fatal: the user has proven their
		// password, so we proceed with token issuance. The stale counter
		// remains and may auto-lock prematurely on the next failure, but
		// the alternative (rejecting a valid login) is worse.
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
		s.logger.Error("sessionlogin:token issuance failed",
			slog.Any("error", err), slog.String("user_id", user.ID))
		return loginOutcome{}, err
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
		return loginOutcome{}, fmt.Errorf("sessionlogin:persist session: %w", err)
	}
	refreshWire, _, err := s.refreshStore.Issue(txCtx, sess.ID, user.ID, user.AuthzEpoch())
	if err != nil {
		s.logger.Error("sessionlogin:refresh store issue failed",
			slog.Any("error", err), slog.String("user_id", user.ID))
		if isNoopTx(s.txRunner) {
			_ = s.sessionStore.Revoke(context.WithoutCancel(txCtx), sess.ID)
		}
		return loginOutcome{}, errcode.Wrap(errcode.KindUnavailable, errcode.ErrAuthRefreshUnavailable, "refresh store unavailable", err)
	}
	if err := outbox.Emit(txCtx, s.emitter, dto.TopicSessionCreated, dto.SessionCreatedEvent{
		SessionID: sess.ID,
		UserID:    user.ID,
	}); err != nil {
		if isNoopTx(s.txRunner) {
			s.cleanupIssuedSession(txCtx, sess.ID)
		}
		return loginOutcome{}, fmt.Errorf("sessionlogin:emit event: %w", err)
	}
	return loginOutcome{
		pair: dto.TokenPair{
			AccessToken:           minted.AccessToken,
			RefreshToken:          refreshWire,
			ExpiresAt:             minted.ExpiresAt,
			SessionID:             sessionID,
			UserID:                user.ID,
			PasswordResetRequired: user.PasswordResetRequired(),
		},
	}, nil
}

// recordFailureBestEffort calls accountlockout.RecordFailure inside the
// existing tx, logging (but not returning) any error so the caller can always
// return the unified 401. The failure modes (counter UPDATE failure, lock
// mutation failure, outbox emit failure) all degrade gracefully: the user is
// still rejected by the 401, only the auto-lockout bookkeeping is incomplete.
//
// reason is a free-form label used in slog Error context to disambiguate
// "wrong_password" vs "baseline_assert" failures; it is NOT exposed on the
// wire (account-status enumeration prevention) and is NOT the metric
// `reason` label (which is fixed by accountlockout to {threshold_locked,
// lazy_unlocked}).
//
// Decision: returning 401 to the caller takes priority over lockout counter
// accuracy. After PR #585 P1#1 fix the failure-path closure returns nil
// from RunInTx so the counter UPDATE (and any threshold-triggered LockUser
// mutation) commits with the rest of the tx. If RecordFailure itself
// errored mid-way, PG places the tx in failed state and Commit translates
// to ROLLBACK at the connection level — the upstream caller then sees a
// 5xx instead of the 401, which is correct: infra failure should surface,
// not be disguised as a credential rejection.
func (s *Service) recordFailureBestEffort(ctx, txCtx context.Context, user *domain.User, reason string) {
	if err := s.lockout.RecordFailure(ctx, txCtx, user); err != nil {
		s.logger.Error("sessionlogin:lockout record failure failed",
			slog.Any("error", err),
			slog.String("user_id", user.ID),
			slog.String("reason", reason))
	}
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
			return fmt.Errorf("sessionlogin:persist session: %w", err)
		}
		wire, _, err := s.refreshStore.Issue(txCtx, sess.ID, userID, authzEpoch)
		if err != nil {
			s.logger.Error("sessionlogin:refresh store issue failed",
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
			return fmt.Errorf("sessionlogin:emit event: %w", err)
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
		s.logger.Error("sessionlogin:cleanup refresh chain failed",
			slog.Any("error", err), slog.String("session_id", sessionID))
	}
	// session.Store.Revoke is idempotent: missing IDs are no-ops returning nil
	// (防枚举 — append-only revoke semantics per ADR-Session D3).
	if err := s.sessionStore.Revoke(cleanupCtx, sessionID); err != nil {
		s.logger.Error("sessionlogin:cleanup session revoke failed",
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
// Why this path returns the specific 403 ErrAuthUserNotActive rather than the
// uniform 401 ErrAuthLoginFailed used by the public Login endpoint: IssueForUser
// is reached only from identitymanage.ChangePassword, which runs under an
// authenticated admin/self caller (the caller has already proven knowledge of
// either an admin token or the user's old password). Account-existence
// enumeration is not a concern here — the caller already knows the user
// exists — so the specific error code surfaces the actual reason for the
// admin/UI to handle. The 401 enumeration-collapse design lives only on the
// public Login endpoint where any unauthenticated requester can probe.
//
// Wire envelope note (#11): after the §A11 funnel rewrite the
// credentialauthority.Assert returns a unified message string
// ("credential not authoritative") for both baseline and version-pin
// failures. The errcode Kind+Code (403 / ErrAuthUserNotActive) is what
// the admin/UI consumer pattern-matches on — message text is opaque.
//
// Returns dto.TokenPair (internal/dto, value not pointer) so this method
// implements the identitymanage.TokenIssuer interface without a cross-slice
// import (F-ARCH-1). Value type makes (nil, nil) unrepresentable.
func (s *Service) IssueForUser(ctx context.Context, userID string) (dto.TokenPair, error) {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return dto.TokenPair{}, fmt.Errorf("sessionlogin:IssueForUser get user: %w", err)
	}
	if err := credentialauthority.Assert(user); err != nil {
		return dto.TokenPair{}, err
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
		s.logger.Error("sessionlogin:IssueForUser token issuance failed",
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

	s.logger.Info("sessionlogin:IssueForUser issued new session",
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
