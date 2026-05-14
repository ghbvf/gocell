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

// Service implements identity management business logic.
type Service struct {
	repo                         ports.UserRepository
	invalidator                  *credentialinvalidate.Invalidator
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
	if s.tokenIssuer == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellMissingTokenIssuer,
			"identity-manage: tokenIssuer is required; wire via WithTokenIssuer")
	}
	if s.lastAdminProtectionRequested {
		if validation.IsNilInterface(s.lastAdminRoleRepo) {
			return nil, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
				"identity-manage: last-admin protection requires a role repository")
		}
		// S4.0: the guard counts *effective* admins (status='active' AND admin
		// role). RoleRepository.CountEffectiveAdmins is the canonical impl;
		// WrapEffectiveAdminCounter produces the sealed
		// domain.EffectiveAdminCounter wrapper that NewLastAdminGuard accepts.
		// Sealed marker prevents structural mis-wiring with CountByRole or any
		// other look-alike at compile time.
		sealedCounter, wrapErr := domain.WrapEffectiveAdminCounter(s.lastAdminRoleRepo)
		if wrapErr != nil {
			return nil, fmt.Errorf("identity-manage: wrap effective-admin counter: %w", wrapErr)
		}
		guard, guardErr := domain.NewLastAdminGuard(sealedCounter)
		if guardErr != nil {
			return nil, fmt.Errorf("identity-manage: last-admin guard: %w", guardErr)
		}
		s.lastAdminGuard = guard
	}
	clock.MustHaveClock(s.clock, "identitymanage.NewService: clock required — use WithClock(c.clk)")
	return s, nil
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
		user.MarkPasswordResetRequired(s.clock.Now())
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

// applyUserUpdate runs the transactional body of Update: fetch, optionally
// guard a status-demotion, apply patch fields, persist, cascade-revoke
// sessions when status demotes from 'active', and publish. Split out to keep
// Update's cognitive complexity within the CLAUDE.md ≤15 budget once the
// S4.0 effective-admin guard + status-demotion cascade were added.
func (s *Service) applyUserUpdate(ctx context.Context, input UpdateInput, actor string) (*domain.User, error) {
	var user *domain.User
	if err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		u, err := s.repo.GetByID(txCtx, input.ID)
		if err != nil {
			return fmt.Errorf("identity-manage: update: %w", err)
		}
		if err := s.guardUpdateStatusDemotion(txCtx, u, input); err != nil {
			return err
		}
		demotedFromActive := isStatusDemotion(u, input)
		applyUpdateFields(u, input, s.clock.Now())
		if err := s.repo.Update(txCtx, u); err != nil {
			return fmt.Errorf("identity-manage: update: %w", err)
		}
		if err := s.cascadeInvalidateOnDemotion(txCtx, u.ID, demotedFromActive); err != nil {
			return err
		}
		user = u
		return s.publish(txCtx, TopicUserUpdated, dto.UserUpdatedEvent{UserID: u.ID, ActorID: actor})
	}); err != nil {
		return nil, err
	}
	return user, nil
}

// isStatusDemotion reports whether input demotes u.Status away from 'active'.
// 'locked' is rejected at the Update entrypoint, so this returns true exactly
// for active→suspended.
func isStatusDemotion(u *domain.User, input UpdateInput) bool {
	return input.Status != nil &&
		u.Status == domain.StatusActive &&
		*input.Status != string(domain.StatusActive)
}

// cascadeInvalidateOnDemotion fixes CRITICAL-1: the old helper only called
// RevokeForSubject + RevokeUser but skipped BumpAuthzEpoch, leaving a
// suspended user's access JWTs valid until natural exp. Now routes through
// the invalidator funnel so authz_epoch bump + session revoke + refresh
// revoke all happen atomically (CREDENTIAL-INVALIDATE-FUNNEL-01).
// suspended semantics are equivalent to Lock.
//
// The boolean gate keeps the no-op cost zero for non-status updates.
func (s *Service) cascadeInvalidateOnDemotion(ctx context.Context, userID string, demoted bool) error {
	if !demoted {
		return nil
	}
	if err := s.invalidator.Apply(ctx, userID, session.CredentialEventLock); err != nil {
		return fmt.Errorf("identity-manage: update invalidate credentials on demotion: %w", err)
	}
	return nil
}

// guardUpdateStatusDemotion enforces the effective-admin invariant when an
// Update would demote an active admin to suspended/locked. Returning a
// precise 403 here avoids falling through to the DB trigger's P0001 (500).
func (s *Service) guardUpdateStatusDemotion(ctx context.Context, u *domain.User, input UpdateInput) error {
	if input.Status == nil || u.Status != domain.StatusActive || *input.Status == string(domain.StatusActive) {
		return nil
	}
	return s.checkLastAdminRemoval(ctx, u.ID, u.Status)
}

// applyUpdateFields applies JSON-merge-patch semantics in-place on u: every
// non-nil field in input overwrites the corresponding field on u. Pure
// function — extracted from Update to keep that method's cognitive complexity
// inside the 15-line CLAUDE.md budget once the RunInTx closure was added.
func applyUpdateFields(u *domain.User, input UpdateInput, now time.Time) {
	if input.Name != nil {
		u.Username = *input.Name
	}
	if input.Email != nil {
		u.Email = *input.Email
	}
	if input.Status != nil {
		u.Status = domain.UserStatus(*input.Status)
	}
	if input.RequirePasswordReset != nil {
		if *input.RequirePasswordReset {
			u.MarkPasswordResetRequired(now)
		} else {
			u.ClearPasswordResetRequired(now)
		}
	}
	u.UpdatedAt = now
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
		if err := s.checkLastAdminRemoval(txCtx, user.ID, user.Status); err != nil {
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

func (s *Service) lockUserAndRevokeSessions(ctx context.Context, id, actor string) error {
	return s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		user, err := s.repo.GetByID(txCtx, id)
		if err != nil {
			return fmt.Errorf("identity-manage: lock: %w", err)
		}
		if err := s.checkLastAdminRemoval(txCtx, user.ID, user.Status); err != nil {
			return err
		}
		user.LockAccount(s.clock.Now())
		if err := s.repo.Update(txCtx, user); err != nil {
			return fmt.Errorf("identity-manage: lock: %w", err)
		}
		// F17: bump authz_epoch + revoke all sessions + revoke refresh chains.
		// Failure must abort the transaction: silently logging would commit the
		// lock flag while leaving stolen access tokens able to call business
		// endpoints until natural expiry — the exact attack vector "Lock" exists
		// to prevent. Routed through funnel (CREDENTIAL-INVALIDATE-FUNNEL-01).
		if err := s.invalidator.Apply(txCtx, id, session.CredentialEventLock); err != nil {
			return fmt.Errorf("identity-manage: lock invalidate credentials: %w", err)
		}
		if err := s.publish(txCtx, TopicUserLocked, dto.UserLockedEvent{UserID: id, ActorID: actor}); err != nil {
			return err
		}
		return nil
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

	if err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		user, err := s.repo.GetByID(txCtx, id)
		if err != nil {
			return fmt.Errorf("identity-manage: unlock: %w", err)
		}
		user.UnlockAccount(s.clock.Now())
		if err := s.repo.Update(txCtx, user); err != nil {
			return fmt.Errorf("identity-manage: unlock: %w", err)
		}
		if err := s.publish(txCtx, TopicUserUnlocked, dto.UserUnlockedEvent{UserID: id, ActorID: actor}); err != nil {
			return err
		}
		return nil
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
