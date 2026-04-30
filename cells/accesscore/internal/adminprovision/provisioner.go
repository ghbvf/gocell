package adminprovision

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// ProvisionOutcome is the result classification returned from Ensure.
//
// Callers decide per outcome whether to persist side effects (credfile, event)
// or surface 409 / silently skip.
type ProvisionOutcome int

const (
	// OutcomeUnknown is the zero value; it is never returned successfully.
	OutcomeUnknown ProvisionOutcome = iota
	// OutcomeCreated means a fresh admin user + role assignment were persisted.
	// Caller may emit user.created event and/or write credential file.
	OutcomeCreated
	// OutcomeAlreadyExists means at least one admin existed at the fast-path
	// CountByRole check; no side effects were performed. Caller returns 409
	// (HTTP) or nil (Lifecycle — silent skip).
	OutcomeAlreadyExists
	// OutcomeRaceSkipped means the fast-path CountByRole read zero admins but
	// a concurrent replica persisted the admin between check and create.
	// No rows were written. Caller treats the same as OutcomeAlreadyExists.
	OutcomeRaceSkipped
	// OutcomeOrphanRecovered means a prior run crashed after UserRepo.Create
	// but before RoleRepo.AssignToUser committed. Ensure re-asserted the
	// password hash and completed AssignToUser. The admin row existed before
	// Ensure ran; the returned user is the recovered row. Callers treat this
	// like OutcomeCreated for side effects because a pending orphan means the
	// previous provisioning sequence did not complete.
	OutcomeOrphanRecovered
)

// ProvisionInput holds the inputs for a single Ensure call.
//
// PasswordHash is pre-hashed by the caller (bcrypt). Provisioner never sees
// plaintext. A duplicate username is recoverable only when the existing row is
// still a pending provisioning row from the same Source.
type ProvisionInput struct {
	Username     string
	Email        string
	PasswordHash []byte
	RequireReset bool
	Source       domain.UserSource
}

// ProvisionResult holds the successful outcome of Ensure.
type ProvisionResult struct {
	User    *domain.User
	Outcome ProvisionOutcome
}

// UUIDGenerator returns a fresh UUID string. Injected for deterministic tests.
type UUIDGenerator func() string

// Provisioner is the shared domain service.
//
// It is caller-tx-neutral: Ensure does not open a transaction; callers wrap
// it with their own TxRunner if atomicity across Ensure + adjacent writes is
// required.
//
// Concurrency: Ensure is serialized through an internal mutex so the
// CountByRole fast-path → Create → Assign sequence is atomic within a single
// process. This closes the read-after-check window that would otherwise let
// two concurrent callers with different usernames both pass the fast-path and
// each persist an admin row (UserRepo's per-username uniqueness does not
// protect against "admin role has two holders"). The mutex is sufficient for
// single-process deployments (demo, single-instance PG). Multi-instance PG
// deployments must layer a cross-process lock on top — the PG adapter is
// expected to acquire pg_advisory_xact_lock at Ensure entry; see
// ADMINPROVISION-DIST-LOCK-01 in docs/backlog.md.
type Provisioner struct {
	mu       sync.Mutex
	userRepo ports.UserRepository
	roleRepo ports.RoleRepository
	logger   *slog.Logger
	newID    UUIDGenerator
}

// NewProvisioner constructs a Provisioner. All dependencies are required;
// passing nil returns an error so mis-wired assemblies fail at startup rather
// than at the first Ensure call.
func NewProvisioner(
	userRepo ports.UserRepository, roleRepo ports.RoleRepository, logger *slog.Logger, newID UUIDGenerator,
) (*Provisioner, error) {
	if userRepo == nil {
		return nil, fmt.Errorf("adminprovision: UserRepository is required")
	}
	if roleRepo == nil {
		return nil, fmt.Errorf("adminprovision: RoleRepository is required")
	}
	if logger == nil {
		return nil, fmt.Errorf("adminprovision: Logger is required")
	}
	if newID == nil {
		return nil, fmt.Errorf("adminprovision: UUIDGenerator is required")
	}
	return &Provisioner{
		userRepo: userRepo,
		roleRepo: roleRepo,
		logger:   logger,
		newID:    newID,
	}, nil
}

// Status reports whether at least one admin user exists.
//
// Infrastructure errors bubble up unchanged so callers can distinguish a known
// "no admin" from a transient RoleRepo outage.
func (p *Provisioner) Status(ctx context.Context) (bool, error) {
	count, err := p.roleRepo.CountByRole(ctx, domain.RoleAdmin)
	if err != nil {
		return false, fmt.Errorf("adminprovision: count admin users: %w", err)
	}
	return count > 0, nil
}

// Ensure idempotently provisions the first admin. It is race-safe across
// concurrent replicas; see ProvisionOutcome for branch semantics.
//
// Steps:
//  1. Fast-path CountByRole: if > 0, return OutcomeAlreadyExists (no I/O writes).
//  2. Ensure admin role exists (tolerate ErrAuthRoleDuplicate).
//  3. Build user with a fresh UUID, persist via UserRepo.Create.
//     - On ErrAuthUserDuplicate: recount admins. > 0 → OutcomeRaceSkipped
//     (concurrent replica finished first). == 0 → only recover if the
//     duplicate row is a pending same-source provisioning orphan.
//  4. AssignToUser(user, admin) — idempotent per port contract.
//  5. Mark the provisioning row complete so future duplicate usernames cannot
//     be reclaimed after the first-admin sequence is done.
func (p *Provisioner) Ensure(ctx context.Context, in ProvisionInput) (ProvisionResult, error) {
	if len(in.PasswordHash) == 0 {
		return ProvisionResult{Outcome: OutcomeUnknown}, fmt.Errorf("adminprovision: PasswordHash is required")
	}
	if !domain.ValidAdminProvisionSource(in.Source) {
		return ProvisionResult{Outcome: OutcomeUnknown}, fmt.Errorf("adminprovision: Source must be setup or bootstrap")
	}

	// Serialize the fast-path → Create → Assign sequence so two concurrent
	// Ensure callers cannot both pass CountByRole==0 and each persist a
	// distinct admin user. Single-process scope only; see Provisioner godoc.
	p.mu.Lock()
	defer p.mu.Unlock()

	// 1. Fast path.
	exists, err := p.Status(ctx)
	if err != nil {
		return ProvisionResult{Outcome: OutcomeUnknown}, err
	}
	if exists {
		p.logger.Debug("admin provision skipped: admin already exists",
			slog.String("event", "admin_provision_skip"))
		return ProvisionResult{Outcome: OutcomeAlreadyExists}, nil
	}

	// 2. Ensure admin role.
	if err := p.ensureAdminRole(ctx); err != nil {
		return ProvisionResult{Outcome: OutcomeUnknown}, err
	}

	// 3. Persist user (with race / orphan detection).
	result, err := p.createUserOrRecover(ctx, in)
	if err != nil || result.Outcome == OutcomeRaceSkipped {
		return result, err
	}

	// 4. Assign admin role (idempotent).
	if _, err := p.roleRepo.AssignToUser(ctx, result.User.ID, domain.RoleAdmin); err != nil {
		return ProvisionResult{Outcome: OutcomeUnknown}, fmt.Errorf("adminprovision: assign admin role: %w", err)
	}
	if err := p.markProvisionComplete(ctx, result.User); err != nil {
		p.rollbackAssignedAdmin(ctx, result.User.ID)
		return ProvisionResult{Outcome: OutcomeUnknown}, err
	}

	return result, nil
}

// Compensate best-effort removes the admin role assignment and user row after
// a post-Ensure side effect (e.g., credfile write) fails. Errors are logged,
// not returned: the operator's immediate concern is the outer failure.
func (p *Provisioner) Compensate(ctx context.Context, userID string) {
	if err := p.roleRepo.RemoveFromUser(ctx, userID, domain.RoleAdmin); err != nil {
		p.logger.Error("admin provision compensate: unassign role failed",
			slog.String("event", "admin_provision_compensate"),
			slog.String("user_id", userID),
			slog.Any("error", err))
	}
	if err := p.userRepo.Delete(ctx, userID); err != nil {
		p.logger.Error("admin provision compensate: delete user failed",
			slog.String("event", "admin_provision_compensate"),
			slog.String("user_id", userID),
			slog.Any("error", err))
		return
	}
	p.logger.Warn("admin provision compensated; retry on next invocation",
		slog.String("event", "admin_provision_compensate"),
		slog.String("user_id", userID))
}

func (p *Provisioner) ensureAdminRole(ctx context.Context) error {
	adminRole := &domain.Role{
		ID:   domain.RoleAdmin,
		Name: domain.RoleAdmin,
		Permissions: []domain.Permission{
			{Resource: "*", Action: "*"},
		},
	}
	if err := p.roleRepo.Create(ctx, adminRole); err != nil {
		var ecErr *errcode.Error
		if !errors.As(err, &ecErr) || ecErr.Code != errcode.ErrAuthRoleDuplicate {
			return fmt.Errorf("adminprovision: ensure admin role: %w", err)
		}
	}
	return nil
}

// createUserOrRecover persists a new admin user or resumes orphan recovery.
// Return convention:
//   - {User:user, Outcome:OutcomeCreated}, nil         — fresh row persisted
//   - {User:user, Outcome:OutcomeOrphanRecovered}, nil — existing orphan row reclaimed
//   - {Outcome:OutcomeRaceSkipped}, nil                — concurrent replica finished first
//   - {Outcome:OutcomeUnknown}, err                    — infra error
func (p *Provisioner) createUserOrRecover(ctx context.Context, in ProvisionInput) (ProvisionResult, error) {
	user, err := domain.NewUser(in.Username, in.Email, string(in.PasswordHash))
	if err != nil {
		return ProvisionResult{Outcome: OutcomeUnknown}, fmt.Errorf("adminprovision: construct user: %w", err)
	}
	user.ID = p.newID()
	user.MarkProvisionPending(in.Source)
	if in.RequireReset {
		user.MarkPasswordResetRequired()
	}

	createErr := p.userRepo.Create(ctx, user)
	if createErr == nil {
		return ProvisionResult{User: user, Outcome: OutcomeCreated}, nil
	}

	var ecErr *errcode.Error
	if !errors.As(createErr, &ecErr) || ecErr.Code != errcode.ErrAuthUserDuplicate {
		return ProvisionResult{Outcome: OutcomeUnknown}, fmt.Errorf("adminprovision: create user: %w", createErr)
	}

	// Duplicate — race or orphan recovery.
	recount, err := p.roleRepo.CountByRole(ctx, domain.RoleAdmin)
	if err != nil {
		return ProvisionResult{Outcome: OutcomeUnknown}, fmt.Errorf("adminprovision: recount after duplicate user: %w", err)
	}
	if recount > 0 {
		p.logger.Debug("admin provision: duplicate user creation race; admin already exists",
			slog.String("event", "admin_provision_race"))
		return ProvisionResult{Outcome: OutcomeRaceSkipped}, nil
	}

	// Orphan recovery.
	existing, err := p.userRepo.GetByUsername(ctx, in.Username)
	if err != nil {
		return ProvisionResult{Outcome: OutcomeUnknown}, fmt.Errorf("adminprovision: lookup orphan user: %w", err)
	}
	if !existing.IsRecoverableProvisionOrphan(in.Source) {
		p.logger.Warn("admin provision: duplicate username is not a recoverable orphan",
			slog.String("event", "admin_provision_duplicate_rejected"),
			slog.String("user_id", existing.ID),
			slog.String("source", string(existing.CreationSource)))
		return ProvisionResult{Outcome: OutcomeUnknown}, errcode.NewDomain(errcode.ErrAuthUserDuplicate,
			"admin provisioning username already exists")
	}
	// Orphan recovery must fully reassert the caller's RequireReset intent,
	// not just "set when true". A setup-path orphan recovery (RequireReset=false)
	// that inherited PasswordResetRequired=true from a crashed bootstrap-path
	// run would otherwise force the operator to reset a password they just
	// chose, contradicting the interactive setup semantics.
	existing.Email = in.Email
	existing.PasswordHash = string(in.PasswordHash)
	existing.PasswordResetRequired = in.RequireReset
	if err := p.userRepo.Update(ctx, existing); err != nil {
		return ProvisionResult{Outcome: OutcomeUnknown}, fmt.Errorf("adminprovision: reset orphan credentials: %w", err)
	}
	p.logger.Info("admin provision: resuming orphan-user recovery",
		slog.String("event", "admin_provision_orphan_recover"),
		slog.String("user_id", existing.ID))
	return ProvisionResult{User: existing, Outcome: OutcomeOrphanRecovered}, nil
}

func (p *Provisioner) markProvisionComplete(ctx context.Context, user *domain.User) error {
	user.MarkProvisionComplete()
	if err := p.userRepo.Update(ctx, user); err != nil {
		return fmt.Errorf("adminprovision: mark provision complete: %w", err)
	}
	return nil
}

func (p *Provisioner) rollbackAssignedAdmin(ctx context.Context, userID string) {
	if err := p.roleRepo.RemoveFromUser(ctx, userID, domain.RoleAdmin); err != nil {
		p.logger.Error("admin provision: rollback assigned role failed",
			slog.String("event", "admin_provision_rollback"),
			slog.String("user_id", userID),
			slog.Any("error", err))
	}
}
