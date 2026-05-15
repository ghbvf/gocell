// Package identitymanage implements the identity-manage slice: CRUD + Lock/Unlock
// user accounts.
package identitymanage

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/cells/accesscore/internal/authzmutate"
	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialinvalidate"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/session"
)

// keep session import used for CredentialEventDelete in deleteUserAndRevokeTokens.
var _ = session.CredentialEventDelete

// TokenIssuer is a narrow interface for issuing a new token pair after a
// password change. The implementation is sessionlogin.Service.IssueForUser,
// injected via WithTokenIssuer to avoid a cross-slice import. The returned
// type is dto.TokenPair (internal/dto, value not pointer) so identitymanage
// does not import sessionlogin directly (F-ARCH-1). Returning a value type
// makes (nil, nil) unrepresentable at the type level.
type TokenIssuer interface {
	IssueForUser(ctx context.Context, userID string) (dto.TokenPair, error)
}

// Topic constants are defined in cells/accesscore/internal/dto to allow sharing
// with the setup slice without either slice importing the other. These locals
// preserve the existing TestXxx(TopicUserCreated...) style in the test suite.
const (
	TopicUserCreated  = dto.TopicUserCreated
	TopicUserLocked   = dto.TopicUserLocked
	TopicUserUpdated  = dto.TopicUserUpdated
	TopicUserDeleted  = dto.TopicUserDeleted
	TopicUserUnlocked = dto.TopicUserUnlocked
)

// actorFromContext extracts the authenticated subject from the request context.
// Admin write paths must have a non-empty Subject; if it is empty this returns
// ErrAuthUnauthorized so downstream emit does not record a blank actor.
func actorFromContext(ctx context.Context) (string, error) {
	p, ok := auth.FromContext(ctx)
	if !ok || p.Subject == "" {
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthUnauthorized,
			"identity-manage: actor required — admin auth must be present")
	}
	return p.Subject, nil
}

// callerHasRole reports whether the authenticated principal in ctx holds the
// given role. Returns false for missing / anonymous principals so callers
// fail-closed on field-level guards (e.g., status-mutation admin-only check).
func callerHasRole(ctx context.Context, role string) bool {
	p, ok := auth.FromContext(ctx)
	if !ok {
		return false
	}
	for _, r := range p.Roles {
		if r == role {
			return true
		}
	}
	return false
}

// Option configures an identity-manage Service.
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

// WithClock sets the clock used for timestamping operations.
// clk must not be nil; pass clock.Real() for production use.
func WithClock(clk clock.Clock) Option {
	return func(s *Service) {
		clock.MustHaveClock(clk, "identitymanage.WithClock")
		s.clock = clk
	}
}

// WithTokenIssuer injects the token issuer used by ChangePassword to issue a
// fresh TokenPair after a successful password change. tokenIssuer must not be
// nil; NewService returns an error if it is not provided or is nil.
func WithTokenIssuer(ti TokenIssuer) Option {
	return func(s *Service) { s.tokenIssuer = ti }
}

// WithLastAdminProtection wires the role repository used to reject operations
// that would remove the final effective admin from the system.
func WithLastAdminProtection(roleRepo ports.RoleRepository) Option {
	return func(s *Service) {
		s.lastAdminProtectionRequested = true
		s.lastAdminRoleRepo = roleRepo
	}
}

// WithAuthzMutator injects the authzmutate.Mutator for credential-weakening
// domain mutations (Lock, Suspend, RequirePasswordReset, etc.). When nil the
// service constructs one from the injected invalidator, repo, and txRunner.
func WithAuthzMutator(m *authzmutate.Mutator) Option {
	return func(s *Service) {
		if m != nil {
			s.authzmutator = m
		}
	}
}

// Service implements identity management business logic.
type Service struct {
	repo                         ports.UserRepository
	invalidator                  *credentialinvalidate.Invalidator
	authzmutator                 *authzmutate.Mutator
	txRunner                     persistence.CellTxManager
	emitter                      outbox.Emitter
	logger                       *slog.Logger
	tokenIssuer                  TokenIssuer
	clock                        clock.Clock
	lastAdminProtectionRequested bool
	lastAdminRoleRepo            ports.RoleRepository
	lastAdminGuard               *domain.LastAdminGuard
}

// NewService creates an identity-manage Service. tokenIssuer is required;
// callers must supply it via WithTokenIssuer. invalidator is required so all
// credential-revocation events (Lock / Delete / ChangePassword / suspension)
// atomically bump authz_epoch + revoke sessions + revoke refresh chains via
// the single funnel (CREDENTIAL-INVALIDATE-FUNNEL-01).
//
// authzmutator: when not injected via WithAuthzMutator, NewService constructs
// one from (invalidator, repo, txRunner). This is intentional composition
// convenience — all three deps are already validated non-nil at this point, so
// the auto-construction cannot fail. The funnel safety is structural (routed
// through authzmutate.Apply, which enforces epoch-bump + revoke), not
// wiring-dependent; injecting a pre-built Mutator is only needed in tests that
// want to substitute a different invalidator or repo. ref: runtime-api.md
// §Option-范式 builder-noop (累加式 builder: nil入参 = no new data, final
// nil resolved at factory; here the factory auto-constructs rather than
// fail-fast because the inputs are provably valid).
func NewService(
	repo ports.UserRepository,
	invalidator *credentialinvalidate.Invalidator,
	logger *slog.Logger,
	opts ...Option,
) (*Service, error) {
	if repo == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "identity-manage: user repository is required")
	}
	if validation.IsNilInterface(invalidator) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "identity-manage: invalidator is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		repo:        repo,
		invalidator: invalidator,
		emitter:     outbox.NewNoopEmitter(),
		logger:      logger,
	}
	for _, o := range opts {
		o(s)
	}
	if s.txRunner == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "identitymanage: TxRunner required; use WithTxManager")
	}
	// Build authzmutator from injected deps if not explicitly provided via WithAuthzMutator.
	if s.authzmutator == nil {
		m, mErr := authzmutate.New(s.invalidator, s.repo, s.txRunner)
		if mErr != nil {
			return nil, fmt.Errorf("identitymanage: build authzmutator: %w", mErr)
		}
		s.authzmutator = m
	}
	if s.tokenIssuer == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellMissingTokenIssuer,
			"identity-manage: tokenIssuer is required; wire via WithTokenIssuer")
	}
	if s.lastAdminProtectionRequested {
		guard, err := buildLastAdminGuard(s.lastAdminRoleRepo)
		if err != nil {
			return nil, err
		}
		s.lastAdminGuard = guard
	}
	clock.MustHaveClock(s.clock, "identitymanage.NewService: clock required — use WithClock(c.clk)")
	return s, nil
}

// buildLastAdminGuard constructs the domain.LastAdminGuard from a role repository.
// It validates the repo is non-nil, wraps it in the sealed EffectiveAdminCounter
// marker, and constructs the guard.
func buildLastAdminGuard(roleRepo ports.RoleRepository) (*domain.LastAdminGuard, error) {
	if validation.IsNilInterface(roleRepo) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"identity-manage: last-admin protection requires a role repository")
	}
	// S4.0: the guard counts *effective* admins (status='active' AND admin
	// role). RoleRepository.CountEffectiveAdmins is the canonical impl;
	// WrapEffectiveAdminCounter produces the sealed
	// domain.EffectiveAdminCounter wrapper that NewLastAdminGuard accepts.
	// Sealed marker prevents structural mis-wiring with CountByRole or any
	// other look-alike at compile time.
	sealedCounter, wrapErr := domain.WrapEffectiveAdminCounter(roleRepo)
	if wrapErr != nil {
		return nil, fmt.Errorf("identity-manage: wrap effective-admin counter: %w", wrapErr)
	}
	guard, guardErr := domain.NewLastAdminGuard(sealedCounter)
	if guardErr != nil {
		return nil, fmt.Errorf("identity-manage: last-admin guard: %w", guardErr)
	}
	return guard, nil
}

// CreateInput holds parameters for creating a user.
type CreateInput struct {
	Username             string
	Email                string
	Password             string
	RequirePasswordReset bool
}

// Create creates a new user and publishes an event.
// The plain-text password is bcrypt-hashed before storage.
//
// Validation order matches setup.CreateAdmin (username → email → password) so
// both code paths reject the same blank input with the same field message,
// avoiding domain-layer error-class drift (audit S-4).
func (s *Service) Create(ctx context.Context, input CreateInput) (*domain.User, error) {
	if err := validation.RequireNotEmpty(errcode.ErrAuthIdentityInvalidInput,
		validation.F("username", input.Username),
		validation.F("email", input.Email),
		validation.F("password", input.Password),
	); err != nil {
		return nil, err
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(input.Password), domain.BcryptCost)
	if err != nil {
		return nil, fmt.Errorf("identity-manage: hash password: %w", err)
	}

	user, err := domain.NewUser(input.Username, input.Email, string(hash), s.clock.Now())
	if err != nil {
		return nil, err
	}

	actor, err := actorFromContext(ctx)
	if err != nil {
		return nil, err
	}

	user.ID = uuid.NewString()
	if input.RequirePasswordReset {
		// Creation-time: no live sessions exist (epoch=1). This is an allowlisted
		// non-funnel site — authzmutate.Apply is for mutating existing principals.
		user.SetPasswordResetRequired(true, s.clock.Now())
	}

	eventPayload := dto.UserCreatedEvent{
		UserID:   user.ID,
		Username: user.Username,
		ActorID:  actor,
	}
	if err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.Create(txCtx, user); err != nil {
			return fmt.Errorf("identity-manage: create: %w", err)
		}
		if err := s.publish(txCtx, TopicUserCreated, eventPayload); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, err
	}

	s.logger.Info("user created", slog.String("user_id", user.ID))
	return user, nil
}

// GetByID retrieves a user by ID.
func (s *Service) GetByID(ctx context.Context, id string) (*domain.User, error) {
	user, err := s.repo.GetByID(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("identity-manage: get: %w", err)
	}
	return user, nil
}

// UpdateInput holds parameters for updating a user (JSON merge patch semantics).
// Nil pointer fields mean "do not update"; non-nil means "set to this value".
type UpdateInput struct {
	ID                   string
	Name                 *string
	Email                *string
	Status               *string
	RequirePasswordReset *bool // nil=no change, true=mark, false=clear
}

// Update modifies user attributes using JSON merge patch semantics:
// only non-nil fields are applied; missing fields are left unchanged.
//
// Read-modify-write atomicity: GetByID, the in-place field application and
// Update share a single RunInTx closure, mirroring Lock/Unlock — a concurrent
// transaction mutating the user between the read and the write would otherwise
// be silently lost (audit S-3 same-pattern, reviewer F7).
//
// The status string is validated before opening the tx: it is a pure input
// check and rejecting invalid values upfront avoids opening a tx that will
// only roll back.
func (s *Service) Update(ctx context.Context, input UpdateInput) (*domain.User, error) {
	if err := validation.RequireNotEmpty(errcode.ErrAuthIdentityInvalidInput,
		validation.F("id", input.ID),
	); err != nil {
		return nil, err
	}
	if input.Status != nil &&
		*input.Status != string(domain.StatusActive) &&
		*input.Status != string(domain.StatusSuspended) {
		// `locked` is intentionally not allowed via Update — it has its own
		// dedicated Lock() endpoint with revoke-cascade semantics. The
		// allowedValues detail keeps the wire payload self-describing without
		// embedding the runtime value into the const-literal message
		// (errcode MESSAGE-CONST-LITERAL-01 archtest).
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrAuthIdentityInvalidInput,
			"status value not allowed in Update; use Lock for the locked state",
			errcode.WithDetails(
				slog.String("field", "status"),
				slog.String("allowedValues", string(domain.StatusActive)+","+string(domain.StatusSuspended)),
			))
	}
	// S4.0 P1-A: status is an admin-only field. The route policy is
	// selfOrAdminPolicy (PATCH allows users to update their own name/email/
	// requirePasswordReset), but allowing a self-PATCH of status would let a
	// suspended user re-activate themselves and defeat the admin's suspend
	// gesture. Field-level guard: any Status mutation requires the actor to
	// hold auth.RoleAdmin. Other fields stay self-editable.
	if input.Status != nil && !callerHasRole(ctx, auth.RoleAdmin) {
		return nil, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthIdentityInvalidInput,
			"updating status requires admin role",
			errcode.WithDetails(slog.String("field", "status")))
	}

	actor, err := actorFromContext(ctx)
	if err != nil {
		return nil, err
	}

	user, err := s.applyUserUpdate(ctx, input, actor)
	if err != nil {
		return nil, err
	}
	s.logger.Info("user updated", slog.String("user_id", user.ID))
	return user, nil
}

// applyUserUpdate runs the body of Update. It handles non-authz field changes
// (name, email) in a plain RunInTx and delegates credential-weakening status
// changes and reset-flag mutations to authzmutate.Apply.
//
// Design: status and requirePasswordReset changes go through authzmutate.Apply
// which opens its own RunInTx (nested via the same tx manager). Non-authz
// fields (name, email) are applied first in a separate tx, then authzmutate
// is called if needed. This keeps each operation atomic while avoiding mixing
// non-credential and credential writes in the same closure.
//
// applyNonAuthzFields excludes status and passwordResetRequired — those fields
// are routed exclusively through authzmutate.Apply (via resolveCredentialMutation).
// Only name, email, and updatedAt are written in the first tx.
//
// TOCTOU trade-off (KNOWN, ACCEPTED): non-authz fields (name, email) are
// written in tx1 and the credential mutation (status, passwordResetRequired)
// is applied in tx2 via authzmutator.Apply. A concurrent write between tx1
// and tx2 could observe a brief intermediate state where name/email have
// changed but status/epoch have not yet been updated. This split is intentional:
//
//   - Non-authz writes are informational; the user remains fully operational
//     in the intermediate window (status is unchanged until tx2 commits).
//   - The security net for the status/epoch window is
//     sessionvalidate.enforceSessionState's CanAuthenticate check (P1.3b
//     defense-in-depth, added this PR): any request from a non-active user
//     is fail-closed at the validate layer regardless of epoch.
//   - Collapsing both writes into a single tx would require authzmutate.Apply
//     to accept an existing tx context, coupling it to the outer tx and
//     significantly raising complexity. The trade-off is accepted.
//
// For correctness the approach is: apply non-authz field changes first (or
// together with name/email), then apply the credential-changing mutation.
// The outer tx handles name/email + event publish; authzmutate handles the
// credential trifecta.
func (s *Service) applyUserUpdate(ctx context.Context, input UpdateInput, actor string) (*domain.User, error) {
	var user *domain.User
	now := s.clock.Now()

	// Determine what credential mutation to apply (if any) before opening tx.
	credMut, err := s.resolveCredentialMutation(ctx, input)
	if err != nil {
		return nil, err
	}

	// Apply non-authz field changes (name, email) inside a transaction that
	// also publishes the event. Status and resetRequired changes that go
	// through authzmutate are applied below.
	if err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		u, err := s.repo.GetByID(txCtx, input.ID)
		if err != nil {
			return fmt.Errorf("identity-manage: update: %w", err)
		}
		if err := s.guardUpdateStatusDemotion(txCtx, u, input); err != nil {
			return err
		}
		applyNonAuthzFields(u, input, now)
		if err := s.repo.Update(txCtx, u); err != nil {
			return fmt.Errorf("identity-manage: update: %w", err)
		}
		user = u
		return s.publish(txCtx, TopicUserUpdated, dto.UserUpdatedEvent{UserID: u.ID, ActorID: actor})
	}); err != nil {
		return nil, err
	}

	// Apply credential mutation via the funnel (after the main tx commits so
	// the mutation's own RunInTx sees the committed state).
	if credMut.ok {
		if err := s.authzmutator.Apply(ctx, input.ID, credMut.m, now); err != nil {
			return nil, fmt.Errorf("identity-manage: update credential mutation: %w", err)
		}
		// Re-fetch user to get the updated epoch/status.
		updated, err := s.repo.GetByID(ctx, input.ID)
		if err != nil {
			return nil, fmt.Errorf("identity-manage: update re-fetch after mutation: %w", err)
		}
		user = updated
	}

	return user, nil
}

// pendingCredMutation carries the result of resolveCredentialMutation.
// ok == false means no credential mutation is needed for this update;
// this struct avoids returning a nil authzmutate.Mutation interface (which
// would trigger the nilnil linter).
type pendingCredMutation struct {
	m  authzmutate.Mutation
	ok bool
}

// resolveCredentialMutation inspects the UpdateInput and returns the
// authzmutate.Mutation that should be applied, wrapped in a pendingCredMutation.
// When pendingCredMutation.ok is false no credential mutation is needed.
func (s *Service) resolveCredentialMutation(ctx context.Context, input UpdateInput) (pendingCredMutation, error) {
	// Check status change.
	if input.Status != nil {
		switch domain.UserStatus(*input.Status) {
		case domain.StatusSuspended:
			return pendingCredMutation{m: authzmutate.SuspendUser{}, ok: true}, nil
		case domain.StatusActive:
			return pendingCredMutation{m: authzmutate.ActivateUser{}, ok: true}, nil
		}
	}
	// Check requirePasswordReset change.
	if input.RequirePasswordReset != nil {
		if *input.RequirePasswordReset {
			// Check if already set (no-op if flag already true).
			u, err := s.repo.GetByID(ctx, input.ID)
			if err != nil {
				return pendingCredMutation{}, fmt.Errorf("identity-manage: resolve mutation get user: %w", err)
			}
			if u.PasswordResetRequired() {
				return pendingCredMutation{}, nil // already set, no mutation needed
			}
			return pendingCredMutation{m: authzmutate.RequirePasswordReset{}, ok: true}, nil
		}
		return pendingCredMutation{m: authzmutate.ClearPasswordReset{}, ok: true}, nil
	}
	return pendingCredMutation{}, nil // no credential fields changed
}

// applyNonAuthzFields applies non-credential field changes (name, email) to u.
// Status and requirePasswordReset are handled by authzmutate.Apply and are NOT
// set here — they go through the funnel.
func applyNonAuthzFields(u *domain.User, input UpdateInput, now time.Time) {
	if input.Name != nil {
		u.Username = *input.Name
	}
	if input.Email != nil {
		u.Email = *input.Email
	}
	u.UpdatedAt = now
}

// guardUpdateStatusDemotion enforces the effective-admin invariant when an
// Update would demote an active admin to suspended. Returning a precise 403
// here avoids falling through to the DB trigger's P0001 (500).
func (s *Service) guardUpdateStatusDemotion(ctx context.Context, u *domain.User, input UpdateInput) error {
	if input.Status == nil || u.Status() != domain.StatusActive || *input.Status == string(domain.StatusActive) {
		return nil
	}
	return s.checkLastAdminRemoval(ctx, u.ID, u.Status())
}

// Delete removes a user. Before the user row is deleted, all sessions and
// refresh-token chains owned by the user are revoked atomically so any
// in-flight access/refresh tokens cannot survive the delete.
func (s *Service) Delete(ctx context.Context, id string) error {
	if err := validation.RequireNotEmpty(errcode.ErrAuthIdentityInvalidInput,
		validation.F("id", id),
	); err != nil {
		return err
	}

	actor, err := actorFromContext(ctx)
	if err != nil {
		return err
	}

	if err := s.deleteUserAndRevokeTokens(ctx, id, actor); err != nil {
		return err
	}

	s.logger.Info("user deleted", slog.String("user_id", id))
	return nil
}

func (s *Service) deleteUserAndRevokeTokens(ctx context.Context, id, actor string) error {
	return s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		// S4.0: fetch the user so the effective-admin guard can use the real
		// status (active vs locked/suspended). Pre-S4.0 the guard didn't need
		// the user record because hasAdminRole was sufficient, but the
		// effective-admin semantics make locked admins removable without the
		// invariant being touched, so we need status to short-circuit.
		user, err := s.repo.GetByID(txCtx, id)
		if err != nil {
			return fmt.Errorf("identity-manage: delete: %w", err)
		}
		if err := s.checkLastAdminRemoval(txCtx, user.ID, user.Status()); err != nil {
			return err
		}
		// Bump authz_epoch + revoke sessions + revoke refresh chains atomically.
		// Routed through funnel (CREDENTIAL-INVALIDATE-FUNNEL-01).
		if err := s.invalidator.Apply(txCtx, id, session.CredentialEventDelete); err != nil {
			return fmt.Errorf("identity-manage: delete invalidate credentials: %w", err)
		}
		if err := s.repo.Delete(txCtx, id); err != nil {
			return fmt.Errorf("identity-manage: delete: %w", err)
		}
		if err := s.publish(txCtx, TopicUserDeleted, dto.UserDeletedEvent{UserID: id, ActorID: actor}); err != nil {
			return err
		}
		return nil
	})
}

// Lock locks a user account and publishes an event.
//
// Read-modify-write atomicity: GetByID, user.LockAccount(), Update, session/refresh
// revoke and the outbox publish all run inside the same RunInTx closure. A
// concurrent transaction that mutates the user between the read and the write
// would otherwise be silently lost (audit S-3).
//
// The transactional body lives in lockUserAndRevokeSessions to keep this
// outer method's cognitive complexity within the CLAUDE.md ≤15 budget that
// the 5-step closure would otherwise blow past (mirrors the
// updatePasswordAndRevokeSessions split used by ChangePassword).
func (s *Service) Lock(ctx context.Context, id string) error {
	if err := validation.RequireNotEmpty(errcode.ErrAuthIdentityInvalidInput,
		validation.F("id", id),
	); err != nil {
		return err
	}
	actor, err := actorFromContext(ctx)
	if err != nil {
		return err
	}
	if err := s.lockUserAndRevokeSessions(ctx, id, actor); err != nil {
		return err
	}
	s.logger.Info("user locked", slog.String("user_id", id))
	return nil
}

// lockUserAndRevokeSessions runs the transactional body of Lock.
//
// TOCTOU trade-off (KNOWN, ACCEPTED): the last-admin guard runs in tx1
// (GetByID + checkLastAdminRemoval) and the credential mutation runs in tx2
// (authzmutator.Apply via RunInTx). A concurrent admin-status change between
// tx1 and tx2 could in theory cause the guard to pass on a stale read. This
// split is intentional:
//
//   - The last-admin guard is an operability/UX guard, NOT a security boundary.
//     Its purpose is to prevent an admin from accidentally locking themselves
//     out of the system. A concurrent bypass in this window has no security
//     consequence: the locked user would simply need to be unlocked by another
//     admin session.
//   - The security net for any status/epoch intermediate window is
//     sessionvalidate.enforceSessionState's CanAuthenticate check (P1.3b
//     defense-in-depth, added in this PR): any request from a non-active user
//     is fail-closed at the validate layer regardless of epoch, making the
//     intermediate window safe from an authentication perspective.
//
// Changing this to a single-tx design would require passing the RunInTx
// context through authzmutate, coupling the event-publish tx to the guard tx
// and significantly raising cognitive complexity. The trade-off is accepted.
func (s *Service) lockUserAndRevokeSessions(ctx context.Context, id, actor string) error {
	now := s.clock.Now()
	// Guard: check last-admin protection before applying the mutation.
	if err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		user, err := s.repo.GetByID(txCtx, id)
		if err != nil {
			return fmt.Errorf("identity-manage: lock guard: %w", err)
		}
		return s.checkLastAdminRemoval(txCtx, user.ID, user.Status())
	}); err != nil {
		return err
	}
	// Apply via funnel: LockUser sets status=locked + bumps epoch + revokes sessions/refresh.
	if err := s.authzmutator.Apply(ctx, id, authzmutate.LockUser{}, now); err != nil {
		return fmt.Errorf("identity-manage: lock: %w", err)
	}
	// Publish event (outside the authzmutate tx — event is informational).
	return s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		return s.publish(txCtx, TopicUserLocked, dto.UserLockedEvent{UserID: id, ActorID: actor})
	})
}

// checkLastAdminRemoval invokes the effective-admin guard for a mutation that
// would remove userID (delete, lock, or status change away from 'active').
//
// userStatus is the user's current Status — required so the guard can short-
// circuit when the user is already not an effective admin (locked/suspended
// users do not contribute to the invariant). Callers that already fetched the
// user pass user.Status; callers that bypass the fetch (only the Update path,
// which fetches inside applyUpdateFields) re-fetch via GetByID.
//
// S4.0 upgrade: the guard counts effective admins (status='active' AND admin
// role) via lastAdminRoleRepo.CountEffectiveAdmins. The "hasAdminRole" leg is
// kept as a fast pre-check so we do not query CountEffectiveAdmins for users
// that don't hold admin at all.
func (s *Service) checkLastAdminRemoval(ctx context.Context, userID string, userStatus domain.UserStatus) error {
	if s.lastAdminGuard == nil {
		return nil
	}
	roles, err := s.lastAdminRoleRepo.GetByUserID(ctx, userID)
	if err != nil {
		return fmt.Errorf("identity-manage: last-admin roles: %w", err)
	}
	hasAdminRole := false
	for _, role := range roles {
		if role != nil && role.ID == auth.RoleAdmin {
			hasAdminRole = true
			break
		}
	}
	// Effective admin = active + admin role. Locked/suspended admins are not
	// counted by the invariant and may be freely removed.
	userIsActiveAdmin := hasAdminRole && userStatus == domain.StatusActive
	if err := s.lastAdminGuard.CheckRemove(ctx, userID, userIsActiveAdmin); err != nil {
		return fmt.Errorf("identity-manage: last-admin: %w", err)
	}
	return nil
}

// Unlock unlocks a user account.
//
// Read-modify-write atomicity: GetByID + user.UnlockAccount() + Update share one
// RunInTx closure so a concurrent mutation between the read and the write
// cannot be silently lost (audit S-3, mirrors Lock).
func (s *Service) Unlock(ctx context.Context, id string) error {
	if err := validation.RequireNotEmpty(errcode.ErrAuthIdentityInvalidInput,
		validation.F("id", id),
	); err != nil {
		return err
	}

	actor, err := actorFromContext(ctx)
	if err != nil {
		return err
	}

	// Apply via funnel: ActivateUser sets status=active (Invalidates()==false,
	// so no epoch-bump — re-activating is additive, ADR §A6).
	if err := s.authzmutator.Apply(ctx, id, authzmutate.ActivateUser{}, s.clock.Now()); err != nil {
		return fmt.Errorf("identity-manage: unlock: %w", err)
	}
	// Publish event.
	if err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		return s.publish(txCtx, TopicUserUnlocked, dto.UserUnlockedEvent{UserID: id, ActorID: actor})
	}); err != nil {
		return err
	}

	s.logger.Info("user unlocked", slog.String("user_id", id))
	return nil
}

// ChangePasswordInput holds the parameters for changing a user password.
type ChangePasswordInput struct {
	UserID      string
	OldPassword string
	NewPassword string
}

// ChangePassword verifies the old password, hashes the new one, clears the
// PasswordResetRequired flag, updates the user, and issues a fresh TokenPair.
//
// Validation order (P1-9 fix: cheap checks before bcrypt to avoid wasted CPU):
//  1. Required-field check (empty userID / oldPassword / newPassword).
//  2. Cheap string equality check (new == old rejected before bcrypt cost).
//  3. bcrypt.CompareHashAndPassword (old password verification).
//  4. Hash new password.
//  5. Persist updated user.
//  6. Issue new TokenPair via tokenIssuer.
//
// Consistency level: L1 (single-cell local transaction, no outbox event).
// The token pair is issued synchronously so the client can replace stale tokens
// without a forced re-login — this is critical when the old token carried
// password_reset_required=true and would be rejected by the middleware.
//
// IssueForUser tx trade-off (F18): IssueForUser is intentionally called
// OUTSIDE the write transaction. It creates a brand-new session that must not
// be swept by the RevokeForSubject call inside the tx; including it in the tx
// would roll back a legitimate new session if token signing fails. The
// observable trade-off is: if IssueForUser fails after the tx commits, the
// password change is durable but the caller must re-login to obtain a token.
// This is preferable to the inverse (rolling back a committed password change
// because signing failed), and consistent with the principle that credential
// rotation should not be undone by a transient signing-key unavailability.
func (s *Service) ChangePassword(ctx context.Context, input ChangePasswordInput) (dto.TokenPair, error) {
	if err := validation.RequireNotEmpty(errcode.ErrAuthIdentityInvalidInput,
		validation.F("id", input.UserID),
		validation.F("oldPassword", input.OldPassword),
		validation.F("newPassword", input.NewPassword),
	); err != nil {
		return dto.TokenPair{}, err
	}

	// Step 2: Cheap equality check before the expensive bcrypt call.
	// An authenticated user submitting new==old is a client error regardless of
	// whether the old password is correct; no bcrypt cost is warranted.
	if input.NewPassword == input.OldPassword {
		return dto.TokenPair{}, errcode.New(errcode.KindInvalid, errcode.ErrAuthLoginInvalidInput, "new password must differ from old password")
	}

	// Steps 3-5 run inside a single transaction so the password write, old-session
	// sweep, and refresh revoke are atomic. IssueForUser stays outside the tx
	// (F18: new session must not be caught by the RevokeForSubject sweep inside the
	// tx, and signing failure should not roll back a committed password change).
	//
	// CAS guard (S6 CHANGEPASSWORD-CONCURRENT-SEMANTICS-01): GetByID inside the tx
	// snapshots user.PasswordVersion; UpdatePassword's WHERE password_version=$expected
	// clause rejects the write if a concurrent change raced us to the commit.
	// The caller receives ErrVersionConflict (HTTP 409) and should reload + retry.
	//
	// bcrypt inside the tx (B-class decision): ChangePassword is low-frequency;
	// the ~100ms bcrypt cost is acceptable inside a short-lived tx, and keeping
	// the hash computation next to the CAS write avoids a TOCTOU window where a
	// concurrent change could replace the hash between hash computation and write.
	var userID string
	err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		id, txErr := s.changePasswordInTx(txCtx, input)
		if txErr != nil {
			return txErr
		}
		userID = id
		return nil
	})
	if err != nil {
		return dto.TokenPair{}, err
	}

	s.logger.Info("user password changed; prior sessions revoked",
		slog.String("user_id", userID))

	// IssueForUser outside tx (F18 rule — see godoc above).
	pair, err := s.tokenIssuer.IssueForUser(ctx, userID)
	if err != nil {
		return dto.TokenPair{}, fmt.Errorf("identity-manage: change-password issue token: %w", err)
	}
	return pair, nil
}

// changePasswordInTx executes the verify-hash-CAS-revoke steps inside an
// active transaction. Caller MUST invoke inside RunInTx. Returns the resolved
// userID so the caller can log and issue a token after the tx commits.
func (s *Service) changePasswordInTx(txCtx context.Context, input ChangePasswordInput) (string, error) {
	user, err := s.repo.GetByID(txCtx, input.UserID)
	if err != nil {
		return "", fmt.Errorf("identity-manage: change-password get user: %w", err)
	}

	// Step 3: Verify old password (expensive — inside tx by design, see ChangePassword godoc).
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(input.OldPassword)); err != nil {
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthLoginFailed, "old password incorrect")
	}

	newHash, err := bcrypt.GenerateFromPassword([]byte(input.NewPassword), domain.BcryptCost)
	if err != nil {
		return "", fmt.Errorf("identity-manage: change-password hash: %w", err)
	}

	// CAS update via narrow signature; caller cannot mutate unrelated fields.
	// resetRequired=false: password just rotated, no reset prompt needed.
	const resetRequired = false
	if _, err := s.repo.UpdatePassword(
		txCtx, user.ID, string(newHash), resetRequired, user.PasswordVersion,
	); err != nil {
		return "", err // ErrVersionConflict on stale view
	}

	// Bump authz_epoch + cascade revocations inside the same tx so that no
	// old session survives the password change. Routed through funnel
	// (CREDENTIAL-INVALIDATE-FUNNEL-01).
	if err := s.invalidator.Apply(txCtx, user.ID, session.CredentialEventPasswordReset); err != nil {
		return "", fmt.Errorf("identity-manage: change-password revoke sessions: %w", err)
	}

	return user.ID, nil
}

func (s *Service) publish(ctx context.Context, topic string, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("identity-manage: marshal event payload: %w", err)
	}
	entry := outbox.Entry{
		ID:        outbox.MustNewEntryID(),
		EventType: topic,
		Payload:   data,
	}
	if err := s.emitter.Emit(ctx, entry); err != nil {
		return fmt.Errorf("identity-manage: emit event: %w", err)
	}
	return nil
}
