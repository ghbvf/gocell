// Package identitymanage implements the identity-manage slice: CRUD + Lock/Unlock
// user accounts.
package identitymanage

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/authzmutate"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/credential"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/credentialauthority"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/credentialinvalidate"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/tenant"
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

// Topic constants are defined in corecells/accesscore/internal/dto to allow sharing
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

// callerHasAdminAuthority reports whether the caller holds admin authority —
// auth.RoleAdmin OR auth.RoleSuperAdmin. It mirrors the authorizationdecide
// baseline adminOrSuperAdmin() PDP condition (role set {RoleAdmin, RoleSuperAdmin})
// so the field-level status guard and the route PDP gate agree on the same
// authority: PR-10c widened user:write to admit super-admin, so a super-admin
// that clears the route gate must not then be blocked by an admin-only field
// check (PR #1974 review F1). Fail-closed via callerHasRole on a missing principal.
func callerHasAdminAuthority(ctx context.Context) bool {
	return callerHasRole(ctx, auth.RoleAdmin) || callerHasRole(ctx, auth.RoleSuperAdmin)
}

// Option configures an identity-manage Service.
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

// WithTokenIssuer injects the token issuer used by ChangePassword to issue a
// fresh TokenPair after a successful password change. tokenIssuer must not be
// nil; NewService returns an error if it is not provided or is nil.
func WithTokenIssuer(ti TokenIssuer) Option {
	return func(s *Service) { s.tokenIssuer = ti }
}

// WithPasswordHasher overrides the password hasher (default
// credential.NewProductionHasher(), cost 12). Tests wire
// credential.NewTestHasher(bcrypt.MinCost). BCRYPT-COST-FUNNEL-01 guards that
// production never reaches the low-cost door. Bare/typed-nil inputs are
// silently ignored (builder-option semantics) so the production default
// survives.
func WithPasswordHasher(h credential.Hasher) Option {
	return func(s *Service) {
		if validation.IsNilInterface(h) {
			return
		}
		s.hasher = h
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
	repo              ports.UserRepository              `gocell:"required"`
	invalidator       *credentialinvalidate.Invalidator `gocell:"required"`
	authzmutator      *authzmutate.Mutator
	txRunner          persistence.CellTxManager `gocell:"required" gocellErr:"identitymanage: TxRunner required; use WithTxManager"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	emitter           outbox.CellEmitter
	logger            *slog.Logger
	tokenIssuer       TokenIssuer `gocell:"required" gocellKind:"KindInternal" gocellCode:"ErrCellMissingTokenIssuer" gocellErr:"identity-manage: tokenIssuer is required; wire via WithTokenIssuer"` //nolint:lll // R2-approved: struct tag for required-dep funnel cannot be split
	clock             clock.Clock
	lastAdminRoleRepo ports.RoleRepository
	lastAdminGuard    *domain.LastAdminGuard
	// hasher is the password hasher. Optional, but its default is a SAFE
	// full-strength default — credential.NewProductionHasher() (cost 12) — not a
	// degraded one like emitter's noop; production is correct without explicit
	// wiring. Tests override via WithPasswordHasher(credential.NewTestHasher(
	// bcrypt.MinCost)) for speed. A bare/typed-nil override is ignored.
	hasher credential.Hasher
}

// NewService creates an identity-manage Service. tokenIssuer is required;
// callers must supply it via WithTokenIssuer. invalidator is required so all
// credential-revocation events (Lock / Delete / ChangePassword / suspension)
// atomically bump authz_epoch + revoke sessions + revoke refresh chains via
// the single funnel (CREDENTIAL-INVALIDATE-FUNNEL-01).
//
// roleRepo is a REQUIRED positional parameter: it wires the
// at-least-one-effective-admin guard (S4.0). A missing guard is now a compile
// error — there is no opt-in option to forget — superseding the former
// WithLastAdminProtection option + IDENTITYMANAGE-LAST-ADMIN-PROTECTION-WIRING-01
// archtest (Medium → Hard). A nil roleRepo is rejected by buildLastAdminGuard,
// matching the required-dep positional + runtime nil-guard pattern (same ceiling
// as the clk positional param). roleRepo is retained on the Service
// (lastAdminRoleRepo) for the hasAdminRole leg in checkLastAdminRemoval.
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
//
// PR-3b (RLS) tenant scope: identitymanage is a POST-AUTH cell (admin/self
// endpoints behind the JWT listener). Its users/roles reads+writes run inside
// bare txRunner.RunInTx and get the app.tenant_id GUC from the authenticated
// principal's ctxkeys.TenantID (JWT claim) via TxManager.tenantScopeForTx
// fallback — it deliberately does NOT use corecells/accesscore/internal/scopedtx
// (that funnel is for PRE-AUTH / service paths that carry no ctxkeys.TenantID).
// Callers must therefore invoke these methods only on a JWT-authenticated ctx.
func NewService(
	clk clock.Clock,
	repo ports.UserRepository,
	invalidator *credentialinvalidate.Invalidator,
	logger *slog.Logger,
	roleRepo ports.RoleRepository,
	opts ...Option,
) (*Service, error) {
	clock.MustHaveClock(clk, "identitymanage.NewService")
	if logger == nil {
		logger = slog.Default()
	}
	s := &Service{
		repo:              repo,
		invalidator:       invalidator,
		clock:             clk,
		emitter:           outbox.DemoCellEmitter(),
		logger:            logger,
		hasher:            credential.NewProductionHasher(),
		lastAdminRoleRepo: roleRepo,
	}
	for _, o := range opts {
		o(s)
	}
	if err := s.validateRequired(); err != nil {
		return nil, err
	}
	// Build authzmutator from injected deps if not explicitly provided via WithAuthzMutator.
	if s.authzmutator == nil {
		m, mErr := authzmutate.New(s.invalidator, s.repo)
		if mErr != nil {
			return nil, fmt.Errorf("identitymanage: build authzmutator: %w", mErr)
		}
		s.authzmutator = m
	}
	// Build the last-admin guard unconditionally — roleRepo is required, so the
	// guard always exists; a nil roleRepo fails fast here.
	guard, err := buildLastAdminGuard(roleRepo)
	if err != nil {
		return nil, err
	}
	s.lastAdminGuard = guard
	return s, nil
}

// buildLastAdminGuard constructs the domain.LastAdminGuard from a role repository.
// It validates the repo is non-nil, wraps it in the sealed EffectiveAdminCounter
// marker, and constructs the guard.
func buildLastAdminGuard(roleRepo ports.RoleRepository) (*domain.LastAdminGuard, error) {
	if roleRepo == nil {
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
	if err := validation.RequireNotEmpty(
		errcode.ErrAuthIdentityInvalidInput,
		validation.F("username", input.Username),
		validation.F("email", input.Email),
		validation.F("password", input.Password),
	); err != nil {
		return nil, err
	}

	hash, err := s.hasher.Hash([]byte(input.Password))
	if err != nil {
		return nil, fmt.Errorf("identity-manage: hash password: %w", err)
	}

	user, err := domain.NewUser(input.Username, input.Email, hash, s.clock.Now())
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

	tid, err := tenant.FromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("identity-manage: create: tenant: %w", err)
	}
	eventPayload := dto.UserCreatedEvent{
		UserID:   user.ID,
		Username: user.Username,
		ActorID:  actor,
	}
	if err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		if err := s.repo.Create(txCtx, tid, user); err != nil {
			return fmt.Errorf("identity-manage: create: %w", err)
		}
		if err := outbox.Emit(txCtx, s.clock, s.emitter, TopicUserCreated, eventPayload); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return nil, err
	}

	s.logger.Info("user created", slog.String("user_id", user.ID))
	return user, nil
}

// GetByID retrieves a user by ID. Tenant-scoped: uses GetByIDInTenant so an
// admin cannot read a user from a different tenant even if they know the UUID.
//
// The read runs inside txRunner.RunInTx (#1617 PR-3b review F1): users is under
// FORCE ROW LEVEL SECURITY (migration 053), so a bare-pool SELECT would be
// fail-closed to 0 rows under the restricted app-serving pool (#1676) unless the
// app.tenant_id GUC is set. RunInTx injects that GUC from the authenticated
// principal's ctxkeys.TenantID (the post-auth fallback documented on NewService)
// — the SAME tid GetByIDInTenant filters on, so GUC and predicate are consistent.
// Every other identitymanage method already wraps its repo access this way;
// GetByID was the lone read that bypassed it.
func (s *Service) GetByID(ctx context.Context, id string) (*domain.User, error) {
	tid, err := tenant.FromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("identity-manage: get: tenant: %w", err)
	}
	var user *domain.User
	if err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		u, gerr := s.repo.GetByIDInTenant(txCtx, tid, id)
		if gerr != nil {
			return fmt.Errorf("identity-manage: get: %w", gerr)
		}
		user = u
		return nil
	}); err != nil {
		return nil, err
	}
	return user, nil
}

// UpdateInput holds parameters for updating a user (JSON merge patch semantics).
// Nil pointer fields mean "do not update"; non-nil means "set to this value".
//
// Name and Email use *domain.NonEmpty rather than *string: the typed wrapper
// makes empty-string PATCH unrepresentable at the type boundary (NonEmpty
// constructor / UnmarshalJSON rejects ""), eliminating the need for runtime
// "name must not be empty" checks in Update.
type UpdateInput struct {
	ID                   string
	Name                 *domain.NonEmpty
	Email                *domain.NonEmpty
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
	if err := validation.RequireNotEmpty(
		errcode.ErrAuthIdentityInvalidInput,
		validation.F("id", input.ID),
	); err != nil {
		return nil, err
	}
	// Empty-string PATCH protection lives in the type system (*domain.NonEmpty
	// constructor + UnmarshalJSON reject ""); no runtime check required here.
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
				errcode.PublicString("field", "status"),
				errcode.PublicString("allowedValues", string(domain.StatusActive)+","+string(domain.StatusSuspended)),
			))
	}
	// S4.0 P1-A: status is an admin-authority field. The route gate is
	// RequirePermissionOrSelf (PATCH lets users update their own name/email/
	// requirePasswordReset), but allowing a self-PATCH of status would let a
	// suspended user re-activate themselves and defeat the admin's suspend
	// gesture. Field-level guard: any Status mutation requires admin authority —
	// admin OR super-admin, the same role set the user:write baseline
	// (adminOrSuperAdmin) grants at the route gate (PR #1974 review F1). Other
	// fields stay self-editable.
	if input.Status != nil && !callerHasAdminAuthority(ctx) {
		return nil, errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthIdentityInvalidInput,
			"updating status requires admin authority",
			errcode.WithDetails(errcode.PublicString("field", "status")))
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
// (name, email) via the narrow UpdateProfile port method and delegates
// credential-weakening status changes and reset-flag mutations to authzmutate.Apply.
//
// Design: all changes (UpdateProfile + credential mutation + event publish) run
// in a single RunInTx closure so that domain mutation and event publish co-commit
// atomically (L2 OutboxFact). Profile writes (username/email) and authz writes
// (status/passwordResetRequired) are separated by port method, not by transaction.
//
// F2 fix: resolveCredentialMutation is invoked inside the tx using the user row
// already fetched by GetByID within the same transaction, eliminating the
// pre-tx GetByID that the old RequirePasswordReset idempotency check used.
// A concurrent BumpAuthzEpoch between a pre-check read and the credential-
// mutation tx would have caused a spurious "no-op", skipping the epoch bump
// and leaving live sessions unrevoked. The single-tx design closes that window.
//
// hasCombinedAuthzFields produces a deterministic 400 and is evaluated before
// the tx — it is a pure-input check that requires no DB read.
func (s *Service) applyUserUpdate(ctx context.Context, input UpdateInput, actor string) (*domain.User, error) {
	// hasCombinedAuthzFields is a pure input check — 400 before any DB access.
	if hasCombinedAuthzFields(input) {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrAuthIdentityInvalidInput,
			msgCombinedAuthzFields)
	}

	now := s.clock.Now()
	var user *domain.User
	// All changes (non-authz fields + credential mutation + event publish) run
	// in a single RunInTx so that domain mutation and event publish co-commit
	// atomically (L2 OutboxFact).
	if err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		var err error
		user, err = s.applyUserUpdateTx(ctx, txCtx, input, actor, now)
		return err
	}); err != nil {
		return nil, err
	}
	return user, nil
}

// applyUserUpdateTx executes the update body inside an already-open transaction.
// It fetches the user row, guards status demotion, writes non-authz profile
// fields via UpdateProfile (when name or email is present), optionally applies
// the credential mutation via authzmutator.ApplyInTx, and publishes the
// UserUpdated event — all in the caller-provided txCtx.
//
// Extracted from applyUserUpdate to keep each function's cognitive complexity ≤ 15.
func (s *Service) applyUserUpdateTx(
	ctx context.Context, txCtx context.Context,
	input UpdateInput, actor string, now time.Time,
) (*domain.User, error) {
	tid, err := tenant.FromContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("identity-manage: update: tenant: %w", err)
	}
	u, err := s.repo.GetByIDForUpdate(txCtx, tid, input.ID)
	if err != nil {
		return nil, fmt.Errorf("identity-manage: update: %w", err)
	}
	if err := s.guardUpdateStatusDemotion(txCtx, tid, u, input); err != nil {
		return nil, err
	}
	// Resolve the credential mutation inside the tx using the already-fetched
	// row, avoiding a separate pre-tx GetByID (F2).
	credMut := resolveCredentialMutationFromUser(u, input)

	// Write profile fields via narrow port method (username / email only).
	// Guard: skip UpdateProfile entirely when neither field is in the PATCH —
	// status-only and requirePasswordReset-only PATCHes must not touch these
	// columns.
	if input.Name != nil || input.Email != nil {
		updated, uerr := s.repo.UpdateProfile(txCtx, tid, input.ID, input.Name, input.Email, now)
		if uerr != nil {
			return nil, fmt.Errorf("identity-manage: update profile: %w", uerr)
		}
		u = updated
	}

	// Apply credential mutation via funnel inside the same tx — L2 OutboxFact:
	// domain mutation, credential invalidation, and event publish co-commit.
	if credMut.ok {
		if err := s.authzmutator.ApplyInTx(ctx, txCtx, tid, input.ID, credMut.m, now); err != nil {
			return nil, fmt.Errorf("identity-manage: update credential mutation: %w", err)
		}
		// Post-mutation re-fetch: tenant-scoped (GetByIDInTenant) so a stale
		// row from another tenant cannot surface here. ForUpdate lock is not
		// needed — the same-tx MVCC snapshot already sees the just-committed
		// writes; FOR UPDATE row-lock semantics apply only to cross-tx concurrency.
		refetched, err := s.repo.GetByIDInTenant(txCtx, tid, input.ID)
		if err != nil {
			return nil, fmt.Errorf("identity-manage: update re-fetch after mutation: %w", err)
		}
		u = refetched
	}
	if err := outbox.Emit(txCtx, s.clock, s.emitter, TopicUserUpdated, dto.UserUpdatedEvent{UserID: input.ID, ActorID: actor}); err != nil {
		return nil, err
	}
	return u, nil
}

// pendingCredMutation carries the result of resolveCredentialMutation.
// ok == false means no credential mutation is needed for this update;
// this struct avoids returning a nil authzmutate.Mutation interface (which
// would trigger the nilnil linter).
type pendingCredMutation struct {
	m  authzmutate.Mutation
	ok bool
}

// msgCombinedAuthzFields is the deterministic error message returned when a
// PATCH request sets both status and requirePasswordReset simultaneously.
// Providing both in one request is ambiguous: the two mutations produce
// different authz_epoch bumps and invalidation events that must be applied
// sequentially, so requiring the client to split them into two requests makes
// the ordering explicit.
const msgCombinedAuthzFields = "status and requirePasswordReset cannot both be set in the same request; send two separate PATCH requests"

// hasCombinedAuthzFields returns true when the input sets both status and
// requirePasswordReset in the same call — an ambiguous combination that must
// be rejected with HTTP 400 before any mutation is attempted.
func hasCombinedAuthzFields(input UpdateInput) bool {
	return input.Status != nil && input.RequirePasswordReset != nil
}

// resolveCredentialMutationFromUser inspects the UpdateInput and the already-
// fetched user row and returns the authzmutate.Mutation that should be applied,
// wrapped in a pendingCredMutation. When pendingCredMutation.ok is false no
// credential mutation is needed.
//
// Precondition: hasCombinedAuthzFields(input) must be false — callers must
// check and return HTTP 400 before calling this function.
//
// The caller passes the user row fetched (and FOR-UPDATE-locked) inside the
// active transaction. This eliminates the previous pre-tx GetByID call for the
// RequirePasswordReset idempotency check, which was an undocumented TOCTOU
// window (F2 fix; see applyUserUpdate godoc for details).
func resolveCredentialMutationFromUser(u *domain.User, input UpdateInput) pendingCredMutation {
	// Check status change.
	if input.Status != nil {
		switch domain.UserStatus(*input.Status) {
		case domain.StatusSuspended:
			return pendingCredMutation{m: authzmutate.SuspendUser{}, ok: true}
		case domain.StatusActive:
			return pendingCredMutation{m: authzmutate.ActivateUser{}, ok: true}
		}
	}
	// Check requirePasswordReset change.
	if input.RequirePasswordReset != nil {
		if *input.RequirePasswordReset {
			// Idempotency: if the flag is already set on the tx-locked row, skip
			// the mutation to avoid a spurious authz_epoch bump (which would
			// invalidate all live sessions unnecessarily).
			if u.PasswordResetRequired() {
				return pendingCredMutation{} // already set, no mutation needed
			}
			return pendingCredMutation{m: authzmutate.RequirePasswordReset{}, ok: true}
		}
		return pendingCredMutation{m: authzmutate.ClearPasswordReset{}, ok: true}
	}
	return pendingCredMutation{} // no credential fields changed
}

// guardUpdateStatusDemotion enforces the effective-admin invariant when an
// Update would demote an active admin to suspended. Returning a precise 403
// here avoids falling through to the DB trigger's P0001 (500).
func (s *Service) guardUpdateStatusDemotion(ctx context.Context, tid tenant.TenantID, u *domain.User, input UpdateInput) error {
	if input.Status == nil || u.Status() != domain.StatusActive || *input.Status == string(domain.StatusActive) {
		return nil
	}
	return s.checkLastAdminRemoval(ctx, tid, u.ID, u.Status())
}

// Delete removes a user. Before the user row is deleted, all sessions and
// refresh-token chains owned by the user are revoked atomically so any
// in-flight access/refresh tokens cannot survive the delete.
func (s *Service) Delete(ctx context.Context, id string) error {
	if err := validation.RequireNotEmpty(
		errcode.ErrAuthIdentityInvalidInput,
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
	tid, err := tenant.FromContext(ctx)
	if err != nil {
		return fmt.Errorf("identity-manage: delete: tenant: %w", err)
	}
	return s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		// S4.0: fetch the user so the effective-admin guard can use the real
		// status (active vs locked/suspended). Tenant-scoped: prevents a
		// cross-tenant admin from deleting a user they merely know the UUID of.
		user, err := s.repo.GetByIDInTenant(txCtx, tid, id)
		if err != nil {
			return fmt.Errorf("identity-manage: delete: %w", err)
		}
		if err := s.checkLastAdminRemoval(txCtx, tid, user.ID, user.Status()); err != nil {
			return err
		}
		// Bump authz_epoch + revoke sessions + revoke refresh chains atomically.
		// Routed through funnel (CREDENTIAL-INVALIDATE-FUNNEL-01).
		if err := s.invalidator.Apply(txCtx, tid, id, session.CredentialEventDelete); err != nil {
			return fmt.Errorf("identity-manage: delete invalidate credentials: %w", err)
		}
		if err := s.repo.Delete(txCtx, tid, id); err != nil {
			return fmt.Errorf("identity-manage: delete: %w", err)
		}
		if err := outbox.Emit(txCtx, s.clock, s.emitter, TopicUserDeleted, dto.UserDeletedEvent{UserID: id, ActorID: actor}); err != nil {
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
	if err := validation.RequireNotEmpty(
		errcode.ErrAuthIdentityInvalidInput,
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
// (GetByID + checkLastAdminRemoval) and the mutation+publish run in tx2
// (ApplyInTx + publish). A concurrent admin-status change between tx1 and
// tx2 could in theory cause the guard to pass on a stale read. This split is
// intentional:
//
//   - The last-admin guard is an operability/UX guard, NOT a security boundary.
//     Its purpose is to prevent an admin from accidentally locking themselves
//     out of the system. A concurrent bypass in this window has no security
//     consequence: the locked user would simply need to be unlocked by another
//     admin session.
//   - The security net for any status/epoch intermediate window is
//     sessionvalidate.enforceSessionState's CanAuthenticate check (P1.3b
//     defense-in-depth): any request from a non-active user is fail-closed at
//     the validate layer regardless of epoch.
//
// Lock/ApplyInTx/publish are in the same RunInTx closure (tx2), satisfying
// the L2 OutboxFact guarantee: domain mutation + event publish co-commit.
func (s *Service) lockUserAndRevokeSessions(ctx context.Context, id, actor string) error {
	tid, err := tenant.FromContext(ctx)
	if err != nil {
		return fmt.Errorf("identity-manage: lock: tenant: %w", err)
	}
	now := s.clock.Now()
	// Guard tx (tx1): check last-admin protection before applying the mutation.
	// Tenant-scoped: admin cannot lock a user from another tenant.
	if err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		user, err := s.repo.GetByIDInTenant(txCtx, tid, id)
		if err != nil {
			return fmt.Errorf("identity-manage: lock guard: %w", err)
		}
		return s.checkLastAdminRemoval(txCtx, tid, user.ID, user.Status())
	}); err != nil {
		return err
	}
	// Apply + publish in a single RunInTx (tx2) — L2 OutboxFact: mutation and
	// event publish co-commit atomically.
	return s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		if err := s.authzmutator.ApplyInTx(ctx, txCtx, tid, id, authzmutate.LockUser{}, now); err != nil {
			return fmt.Errorf("identity-manage: lock: %w", err)
		}
		return outbox.Emit(txCtx, s.clock, s.emitter, TopicUserLocked, dto.UserLockedEvent{UserID: id, ActorID: actor})
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
func (s *Service) checkLastAdminRemoval(ctx context.Context, tid tenant.TenantID, userID string, userStatus domain.UserStatus) error {
	// No nil-guard on s.lastAdminGuard: NewService builds it unconditionally
	// from the required roleRepo positional param (a nil roleRepo fails
	// construction), so a *Service that reaches this method always holds a
	// non-nil guard. The former `if s.lastAdminGuard == nil { return nil }`
	// opt-out skip belonged to the deleted WithLastAdminProtection era and
	// would silently bypass S4.0 — its removal is what makes the guard Hard.
	roles, err := s.lastAdminRoleRepo.GetByUserID(ctx, tid, userID)
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
	if err := s.lastAdminGuard.CheckRemove(ctx, tid, userID, userIsActiveAdmin); err != nil {
		// Application-layer guard blocked the removal — the expected S4.0 path.
		// This log lets ops distinguish an app-layer 403 from a DB-trigger 403:
		// the migration-047 trigger only fires when this guard did NOT (the
		// tx1-check / tx2-mutate TOCTOU window of Lock/Update), so a 403 WITHOUT
		// this log line indicates the trigger safety net caught a concurrent race.
		s.logger.Info("last-admin guard blocked removal",
			slog.String("user_id", userID),
			slog.String("user_status", string(userStatus)))
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
	if err := validation.RequireNotEmpty(
		errcode.ErrAuthIdentityInvalidInput,
		validation.F("id", id),
	); err != nil {
		return err
	}

	actor, err := actorFromContext(ctx)
	if err != nil {
		return err
	}

	tid, err := tenant.FromContext(ctx)
	if err != nil {
		return fmt.Errorf("identity-manage: unlock: tenant: %w", err)
	}
	// Apply + publish in a single RunInTx — L2 OutboxFact: ActivateUser mutation
	// and event publish co-commit atomically. Invalidates()==false so no
	// epoch-bump — re-activating is additive, ADR §A6.
	now := s.clock.Now()
	if err := s.txRunner.RunInTx(ctx, func(txCtx context.Context) error {
		if err := s.authzmutator.ApplyInTx(ctx, txCtx, tid, id, authzmutate.ActivateUser{}, now); err != nil {
			return fmt.Errorf("identity-manage: unlock: %w", err)
		}
		return outbox.Emit(txCtx, s.clock, s.emitter, TopicUserUnlocked, dto.UserUnlockedEvent{UserID: id, ActorID: actor})
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
//  3. Inactive-account gate (credentialauthority.Assert, #1017) — runs first
//     inside the tx, before bcrypt/UpdatePassword, so a suspended/locked
//     account's credential is never rewritten (see changePasswordInTx).
//  4. bcrypt.CompareHashAndPassword (old password verification).
//  5. Hash new password.
//  6. Persist updated user.
//  7. Issue new TokenPair via tokenIssuer.
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
	if err := validation.RequireNotEmpty(
		errcode.ErrAuthIdentityInvalidInput,
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
	// Two-layer active guard (#1017): the read-time credentialauthority.Assert
	// below gives the fast 403 for an account already inactive at read time (no
	// wasted bcrypt). The write-time backstop is UpdatePassword's
	// `WHERE ... AND status = 'active'` predicate (#1017 F1): if a concurrent
	// Lock/Suspend commits between this non-locking GetByID and the write, the
	// UPDATE matches 0 rows and UpdatePassword returns ErrAuthUserNotActive — the
	// now-frozen account's credential is NOT rewritten. The write predicate, not a
	// row lock, closes the concurrent-freeze window, so concurrent ChangePassword
	// requests keep their CAS-conflict semantics (below) rather than serializing.
	//
	// CAS guard (S6 CHANGEPASSWORD-CONCURRENT-SEMANTICS-01): GetByID inside the tx
	// snapshots user.PasswordVersion; UpdatePassword's WHERE password_version=$expected
	// clause rejects the write if a concurrent ChangePassword raced us to the commit.
	// The caller receives ErrVersionConflict (HTTP 409) and should reload + retry.
	// Status guard and version guard are orthogonal predicates on the same UPDATE:
	// a 0-row result is disambiguated (inactive → 403; version mismatch → 409).
	//
	// bcrypt inside the tx (B-class decision): ChangePassword is low-frequency;
	// the ~100ms bcrypt cost is acceptable inside a short-lived tx, and keeping
	// the hash computation next to the CAS write avoids a TOCTOU window where a
	// concurrent change could replace the hash between hash computation and write.
	//
	// mem path outside a live tx (foreign / non-locking TxRunner): GetByID and
	// UpdatePassword each acquire store.mu independently (per-call), so bcrypt
	// runs between the two locks rather than under a held lock. Cross-method
	// atomicity is only guaranteed by the mem Store's own TxRunner (live lease)
	// and by PG; the CAS version check + status predicate still guard correctness
	// on the mem path (ADR 202605171846).
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
	tid, err := tenant.FromContext(txCtx)
	if err != nil {
		return "", fmt.Errorf("identity-manage: change-password: tenant: %w", err)
	}
	user, err := s.repo.GetByIDInTenant(txCtx, tid, input.UserID)
	if err != nil {
		return "", fmt.Errorf("identity-manage: change-password get user: %w", err)
	}

	// Inactive-account gate BEFORE any credential mutation (issue #1017).
	// A suspended/locked account must never have its password rewritten, so the
	// active check fails-closed here — before the bcrypt verify, the CAS write,
	// and the session sweep — rather than post-commit inside IssueForUser. The
	// IssueForUser Assert is retained as belt-and-braces for the narrow window
	// where an admin freezes the account between this commit and the token issue.
	//
	// GetByID returns a non-nil user or an error (handled above), so the only
	// reachable Assert failure here is the baseline CanAuthenticate check — the
	// nil-user / nil-Check arms of Assert are unreachable on this path. Mapping
	// every Assert error to ErrAuthUserNotActive is therefore correct and mirrors
	// sessionlogin.IssueForUser's (Assert-gate → 403) shape, so both paths return
	// an identical wire envelope. Timing note: this gate returns before the
	// ~100ms bcrypt, so an inactive account is rejected faster than an active
	// wrong-password — an accepted trade-off on this authenticated endpoint
	// (the caller already proved account existence via its token; see ADR §A17).
	if err := credentialauthority.Assert(user); err != nil {
		s.logger.Warn("change-password: inactive account gate rejected",
			slog.String("user_id", user.ID))
		return "", errcode.New(errcode.KindPermissionDenied, errcode.ErrAuthUserNotActive,
			"account is not active",
			errcode.WithInternal(errcode.InternalAttr("_", "identity-manage: change-password baseline assert failed")))
	}

	// Step 3: Verify old password (expensive — inside tx by design, see ChangePassword godoc).
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(input.OldPassword)); err != nil {
		return "", errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthOldPasswordIncorrect, "old password incorrect")
	}

	newHash, err := s.hasher.Hash([]byte(input.NewPassword))
	if err != nil {
		return "", fmt.Errorf("identity-manage: change-password hash: %w", err)
	}

	// CAS update via narrow signature; caller cannot mutate unrelated fields.
	// resetRequired=false: password just rotated, no reset prompt needed.
	const resetRequired = false
	if _, err := s.repo.UpdatePassword(
		txCtx, tid, user.ID, newHash, resetRequired, user.PasswordVersion,
	); err != nil {
		return "", err // ErrVersionConflict on stale view
	}

	// Bump authz_epoch + cascade revocations inside the same tx so that no
	// old session survives the password change. Routed through funnel
	// (CREDENTIAL-INVALIDATE-FUNNEL-01).
	if err := s.invalidator.Apply(txCtx, tid, user.ID, session.CredentialEventPasswordReset); err != nil {
		return "", fmt.Errorf("identity-manage: change-password revoke sessions: %w", err)
	}

	return user.ID, nil
}
