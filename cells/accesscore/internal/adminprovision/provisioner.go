package adminprovision

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
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
)

// ProvisionInput holds the inputs for a single Ensure call.
//
// PasswordHash is pre-hashed by the caller (bcrypt). Provisioner never sees
// plaintext. A duplicate username returns 409 ErrAuthUserDuplicate; the caller
// must use a unique username (setup path enforces this at HTTP layer).
type ProvisionInput struct {
	Username     string
	Email        string
	PasswordHash []byte
	RequireReset bool
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
// Concurrency: Ensure is serialized through an internal mutex for in-memory
// deployments. PG role repositories additionally implement a narrow
// transaction-scoped advisory lock interface detected by this package, so the
// CountByRole fast-path → Create → Assign sequence is serialized across pods.
// The product invariant is "at least one admin exists"; multiple admin holders
// are allowed after setup through normal RBAC assignment.
type Provisioner struct {
	mu       sync.Mutex
	userRepo ports.UserRepository
	roleRepo ports.RoleRepository
	logger   *slog.Logger
	newID    UUIDGenerator
	clock    clock.Clock
}

type provisionLock interface {
	LockAdminProvision(ctx context.Context) error
}

// NewProvisioner constructs a Provisioner. All dependencies are required;
// passing nil returns an error so mis-wired assemblies fail at startup rather
// than at the first Ensure call.
func NewProvisioner(
	userRepo ports.UserRepository, roleRepo ports.RoleRepository, logger *slog.Logger, newID UUIDGenerator, clk clock.Clock,
) (*Provisioner, error) {
	if userRepo == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "adminprovision: UserRepository is required")
	}
	if roleRepo == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "adminprovision: RoleRepository is required")
	}
	if logger == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "adminprovision: Logger is required")
	}
	if newID == nil {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed, "adminprovision: UUIDGenerator is required")
	}
	clock.MustHaveClock(clk, "adminprovision.NewProvisioner")
	return &Provisioner{
		userRepo: userRepo,
		roleRepo: roleRepo,
		logger:   logger,
		newID:    newID,
		clock:    clk,
	}, nil
}

// Status reports whether at least one admin user exists.
//
// Infrastructure errors bubble up unchanged so callers can distinguish a known
// "no admin" from a transient RoleRepo outage.
func (p *Provisioner) Status(ctx context.Context) (bool, error) {
	count, err := p.roleRepo.CountByRole(ctx, auth.RoleAdmin)
	if err != nil {
		return false, fmt.Errorf("adminprovision: count admin users: %w", err)
	}
	return count > 0, nil
}

// Ensure idempotently provisions the first admin. It is race-safe across
// concurrent replicas when the RoleRepository implements the internal
// provisionLock interface; see ProvisionOutcome for branch semantics.
//
// Steps:
//  1. Acquire the optional cross-process provision lock.
//  2. Fast-path CountByRole: if > 0, return OutcomeAlreadyExists (no I/O writes).
//  3. Ensure admin role exists (tolerate ErrAuthRoleDuplicate).
//  4. Build user with a fresh UUID, persist via UserRepo.Create.
//     - On ErrAuthUserDuplicate: recount admins. > 0 → OutcomeRaceSkipped
//     (concurrent replica finished first). == 0 → 409 ErrAuthUserDuplicate
//     (username conflict, operator must use a different username).
//  5. AssignToUser(user, admin) — idempotent per port contract.
func (p *Provisioner) Ensure(ctx context.Context, in ProvisionInput) (ProvisionResult, error) {
	if len(in.PasswordHash) == 0 {
		return ProvisionResult{Outcome: OutcomeUnknown}, fmt.Errorf("adminprovision: PasswordHash is required")
	}

	// Serialize the fast-path → Create → Assign sequence so two concurrent
	// Ensure callers cannot both pass CountByRole==0 and each persist a
	// distinct admin user. Single-process scope only; see Provisioner godoc.
	p.mu.Lock()
	defer p.mu.Unlock()

	if err := p.lockProvision(ctx); err != nil {
		return ProvisionResult{Outcome: OutcomeUnknown}, err
	}

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

	// 3. Persist user (with race detection).
	result, err := p.createAdminUser(ctx, in)
	if err != nil || result.Outcome == OutcomeRaceSkipped {
		return result, err
	}

	// 4. Assign admin role (idempotent).
	if _, err := p.roleRepo.AssignToUser(ctx, result.User.ID, auth.RoleAdmin); err != nil {
		return p.handleAssignAdminError(ctx, err)
	}

	return result, nil
}

func (p *Provisioner) lockProvision(ctx context.Context) error {
	locker, ok := p.roleRepo.(provisionLock)
	if !ok {
		return nil
	}
	if err := locker.LockAdminProvision(ctx); err != nil {
		return fmt.Errorf("adminprovision: lock admin provision: %w", err)
	}
	return nil
}

// handleAssignAdminError folds an AssignToUser failure observed during Ensure
// step 5 into a ProvisionResult. It primarily exists for defensive folding of
// legacy or fake repositories that return ErrAuthRoleDuplicate during an admin
// race; PG's current protocol prevents this by taking LockAdminProvision first.
func (p *Provisioner) handleAssignAdminError(ctx context.Context, err error) (ProvisionResult, error) {
	var ecErr *errcode.Error
	if errors.As(err, &ecErr) && ecErr.Code == errcode.ErrAuthRoleDuplicate {
		cnt, cntErr := p.roleRepo.CountByRole(ctx, auth.RoleAdmin)
		if cntErr != nil {
			return ProvisionResult{Outcome: OutcomeUnknown}, fmt.Errorf("adminprovision: recount after assign duplicate: %w", cntErr)
		}
		if cnt >= 1 {
			p.logger.Debug("admin provision: assign role race; admin already exists",
				slog.String("event", "admin_provision_assign_race"))
			return ProvisionResult{Outcome: OutcomeRaceSkipped}, nil
		}
		// cnt==0 but received 23505 — should not happen; surface as infra error.
	}
	return ProvisionResult{Outcome: OutcomeUnknown}, fmt.Errorf("adminprovision: assign admin role: %w", err)
}

// Compensate best-effort removes the admin role assignment and user row after
// a post-Ensure side effect (e.g., credfile write) fails. Errors are logged,
// not returned: the operator's immediate concern is the outer failure.
func (p *Provisioner) Compensate(ctx context.Context, userID string) {
	if err := p.roleRepo.RemoveFromUser(ctx, userID, auth.RoleAdmin); err != nil {
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
		ID:   auth.RoleAdmin,
		Name: auth.RoleAdmin,
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

// createAdminUser persists a new admin user with race detection.
// Return convention:
//   - {User:user, Outcome:OutcomeCreated}, nil  — fresh row persisted
//   - {Outcome:OutcomeRaceSkipped}, nil          — concurrent replica finished first
//   - {Outcome:OutcomeUnknown}, err              — infra error or username conflict (409)
func (p *Provisioner) createAdminUser(ctx context.Context, in ProvisionInput) (ProvisionResult, error) {
	now := p.clock.Now()
	user, err := domain.NewUser(in.Username, in.Email, string(in.PasswordHash), now)
	if err != nil {
		return ProvisionResult{Outcome: OutcomeUnknown}, fmt.Errorf("adminprovision: construct user: %w", err)
	}
	user.ID = p.newID()
	user.CreationSource = domain.UserSourceSetup
	if in.RequireReset {
		user.MarkPasswordResetRequired(now)
	}

	createErr := p.userRepo.Create(ctx, user)
	if createErr == nil {
		return ProvisionResult{User: user, Outcome: OutcomeCreated}, nil
	}

	var ecErr *errcode.Error
	if !errors.As(createErr, &ecErr) || ecErr.Code != errcode.ErrAuthUserDuplicate {
		return ProvisionResult{Outcome: OutcomeUnknown}, fmt.Errorf("adminprovision: create user: %w", createErr)
	}

	// Duplicate — distinguish race vs true conflict.
	recount, err := p.roleRepo.CountByRole(ctx, auth.RoleAdmin)
	if err != nil {
		return ProvisionResult{Outcome: OutcomeUnknown}, fmt.Errorf("adminprovision: recount after duplicate user: %w", err)
	}
	if recount > 0 {
		// Concurrent replica completed provisioning between our fast-path check and Create.
		p.logger.Debug("admin provision: duplicate user creation race; admin already exists",
			slog.String("event", "admin_provision_race"))
		return ProvisionResult{Outcome: OutcomeRaceSkipped}, nil
	}

	// True conflict: username already taken, no admin role yet. Return 409.
	return ProvisionResult{Outcome: OutcomeUnknown}, errcode.New(errcode.KindConflict, errcode.ErrAuthUserDuplicate,
		"admin provisioning username already exists")
}
