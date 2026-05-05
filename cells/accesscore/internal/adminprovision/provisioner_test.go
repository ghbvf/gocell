package adminprovision_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/cells/accesscore/internal/adminprovision"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func fixedUUID(ids ...string) adminprovision.UUIDGenerator {
	i := -1
	return func() string {
		i++
		if i >= len(ids) {
			return "id-default"
		}
		return ids[i]
	}
}

func stdInput() adminprovision.ProvisionInput {
	return adminprovision.ProvisionInput{
		Username:     "admin",
		Email:        "admin@local",
		PasswordHash: []byte("$2a$10$stubhash0000000000000000000000000000000000000000"),
		RequireReset: true,
	}
}

func ensureForTest(
	p *adminprovision.Provisioner,
	ctx context.Context,
	in adminprovision.ProvisionInput,
) (*domain.User, adminprovision.ProvisionOutcome, error) {
	result, err := p.Ensure(ctx, in)
	return result.User, result.Outcome, err
}

// --- NewProvisioner -------------------------------------------------------

// TestNewProvisioner_NilDeps_ReturnsErrcode is the F3 RED test (table-driven):
// all four nil deps must return errcode.Error (KindInvalid+ErrValidationFailed),
// not bare fmt.Errorf. Will fail until provisioner.go nil checks use errcode.New.
func TestNewProvisioner_NilDeps_ReturnsErrcode(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		user ports.UserRepository
		role ports.RoleRepository
		log  *slog.Logger
		id   adminprovision.UUIDGenerator
	}{
		{name: "nil user repo", user: nil, role: mem.NewRoleRepository(), log: discardLogger(), id: fixedUUID("x")},
		{name: "nil role repo", user: mem.NewUserRepository(), role: nil, log: discardLogger(), id: fixedUUID("x")},
		{name: "nil logger", user: mem.NewUserRepository(), role: mem.NewRoleRepository(), log: nil, id: fixedUUID("x")},
		{name: "nil uuid gen", user: mem.NewUserRepository(), role: mem.NewRoleRepository(), log: discardLogger(), id: nil},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := adminprovision.NewProvisioner(tc.user, tc.role, tc.log, tc.id, clock.Real())
			require.Error(t, err)
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec, "nil dep must return errcode.Error, got: %T: %v", err, err)
			assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
		})
	}
}

// TestEnsure_DuplicateUsername_Returns409 is the Wave 2 RED test:
// after orphan recovery is removed, a duplicate username (non-race, no admin yet)
// must return 409 ErrAuthUserDuplicate without any recovery attempt.
func TestEnsure_DuplicateUsername_Returns409(t *testing.T) {
	t.Parallel()
	userRepo := mem.NewUserRepository()
	existing, err := domain.NewUser("admin", "admin@local", "$2a$10$identityhash", time.Now())
	require.NoError(t, err)
	existing.ID = "usr-existing"
	require.NoError(t, userRepo.Create(context.Background(), existing))

	roleRepo := mem.NewRoleRepository()
	p := newProvisioner(t, userRepo, roleRepo, fixedUUID("y"))

	// Use setup source (no bootstrap source), no admin role yet.
	in := adminprovision.ProvisionInput{
		Username:     "admin",
		Email:        "admin@local",
		PasswordHash: []byte("$2a$10$newhash000000000000000000000000000000000000000000"),
	}
	result, err := p.Ensure(context.Background(), in)
	require.Error(t, err)
	assert.Equal(t, adminprovision.OutcomeUnknown, result.Outcome)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthUserDuplicate, ec.Code)
	// Existing user must be untouched.
	refreshed, getErr := userRepo.GetByID(context.Background(), "usr-existing")
	require.NoError(t, getErr)
	assert.Equal(t, "$2a$10$identityhash", refreshed.PasswordHash, "existing user hash must not change")
}

// TestEnsure_RaceDetected_ReturnsRaceSkipped verifies race detection still works
// after orphan recovery is removed. The duplicate Create + recount>0 path must
// return OutcomeRaceSkipped without error.
func TestEnsure_RaceDetected_ReturnsRaceSkipped(t *testing.T) {
	t.Parallel()
	userRepo := &duplicateUserRepo{}
	roleRepo := &scriptedRoleRepo{counts: []int{0, 1}}
	p := newProvisioner(t, userRepo, roleRepo, fixedUUID("z"))

	in := adminprovision.ProvisionInput{
		Username:     "admin",
		Email:        "admin@local",
		PasswordHash: []byte("$2a$10$hash"),
	}
	result, err := p.Ensure(context.Background(), in)
	require.NoError(t, err)
	assert.Equal(t, adminprovision.OutcomeRaceSkipped, result.Outcome)
	assert.Nil(t, result.User)
}

func TestNewProvisioner_NilDependency_ReturnsError(t *testing.T) {
	tests := []struct {
		name string
		user ports.UserRepository
		role ports.RoleRepository
		log  *slog.Logger
		id   adminprovision.UUIDGenerator
	}{
		{name: "nil user repo", user: nil, role: mem.NewRoleRepository(), log: discardLogger(), id: fixedUUID("x")},
		{name: "nil role repo", user: mem.NewUserRepository(), role: nil, log: discardLogger(), id: fixedUUID("x")},
		{name: "nil logger", user: mem.NewUserRepository(), role: mem.NewRoleRepository(), log: nil, id: fixedUUID("x")},
		{name: "nil uuid gen", user: mem.NewUserRepository(), role: mem.NewRoleRepository(), log: discardLogger(), id: nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := adminprovision.NewProvisioner(tc.user, tc.role, tc.log, tc.id, clock.Real())
			require.Error(t, err)
		})
	}
}

// --- Status ---------------------------------------------------------------

func TestProvisioner_Status_NoAdmin_ReturnsFalse(t *testing.T) {
	p := newProvisioner(t, mem.NewUserRepository(), mem.NewRoleRepository(), fixedUUID("x"))
	has, err := p.Status(context.Background())
	require.NoError(t, err)
	assert.False(t, has)
}

func TestProvisioner_Status_WithAdmin_ReturnsTrue(t *testing.T) {
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	seedAdmin(t, userRepo, roleRepo, "usr-seed")
	p := newProvisioner(t, userRepo, roleRepo, fixedUUID("x"))
	has, err := p.Status(context.Background())
	require.NoError(t, err)
	assert.True(t, has)
}

func TestProvisioner_Status_InfraError_Surfaced(t *testing.T) {
	roleRepo := &errRoleRepo{countErr: errors.New("boom")}
	p := newProvisioner(t, mem.NewUserRepository(), roleRepo, fixedUUID("x"))
	_, err := p.Status(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "count admin users")
	assert.ErrorContains(t, err, "boom")
}

// --- Ensure ---------------------------------------------------------------

func TestProvisioner_Ensure_FreshSystem_CreatesUserAndRole(t *testing.T) {
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	p := newProvisioner(t, userRepo, roleRepo, fixedUUID("00000000-0000-4000-8000-000000000001"))

	user, outcome, err := ensureForTest(p, context.Background(), stdInput())
	require.NoError(t, err)
	assert.Equal(t, adminprovision.OutcomeCreated, outcome)
	require.NotNil(t, user)
	_, parseErr := uuid.Parse(user.ID)
	assert.NoError(t, parseErr, "user ID must be a valid UUID")
	assert.True(t, user.PasswordResetRequired)
	assert.Equal(t, domain.UserSourceSetup, user.CreationSource)
	// Role assigned
	cnt, err := roleRepo.CountByRole(context.Background(), auth.RoleAdmin)
	require.NoError(t, err)
	assert.Equal(t, 1, cnt)
}

func TestProvisioner_Ensure_AdminExists_FastPathSkipsNoWrites(t *testing.T) {
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	seedAdmin(t, userRepo, roleRepo, "usr-prior")

	// Wrap repos to observe that Create is never called on the fast path.
	counting := &countingUserRepo{UserRepository: userRepo}
	p := newProvisioner(t, counting, roleRepo, fixedUUID("x"))

	user, outcome, err := ensureForTest(p, context.Background(), stdInput())
	require.NoError(t, err)
	assert.Equal(t, adminprovision.OutcomeAlreadyExists, outcome)
	assert.Nil(t, user)
	assert.Equal(t, 0, counting.creates, "no user write on fast path")
}

func TestProvisioner_Ensure_RaceSkipped_NoCreatedUserReturned(t *testing.T) {
	// Fake user repo: Create returns ErrAuthUserDuplicate.
	// Fake role repo: CountByRole returns 0 first (fast-path), 1 on recount.
	userRepo := &duplicateUserRepo{}
	roleRepo := &scriptedRoleRepo{counts: []int{0, 1}}
	p := newProvisioner(t, userRepo, roleRepo, fixedUUID("x"))

	user, outcome, err := ensureForTest(p, context.Background(), stdInput())
	require.NoError(t, err)
	assert.Equal(t, adminprovision.OutcomeRaceSkipped, outcome)
	assert.Nil(t, user)
	assert.False(t, roleRepo.assignCalled, "AssignToUser must NOT run on race path")
}

func TestProvisioner_Ensure_RecountAfterDuplicateFails_Surfaced(t *testing.T) {
	// Duplicate Create, but the recount itself errors.
	userRepo := &duplicateUserRepo{}
	roleRepo := &recountErrRoleRepo{firstCount: 0, recountErr: errors.New("recount failed")}
	p := newProvisioner(t, userRepo, roleRepo, fixedUUID("x"))
	_, outcome, err := ensureForTest(p, context.Background(), stdInput())
	require.Error(t, err)
	assert.Equal(t, adminprovision.OutcomeUnknown, outcome)
	assert.ErrorContains(t, err, "recount after duplicate user")
}

func TestProvisioner_Ensure_RoleRepoCountError_Surfaced(t *testing.T) {
	roleRepo := &errRoleRepo{countErr: errors.New("boom")}
	p := newProvisioner(t, mem.NewUserRepository(), roleRepo, fixedUUID("x"))
	_, outcome, err := ensureForTest(p, context.Background(), stdInput())
	require.Error(t, err)
	assert.Equal(t, adminprovision.OutcomeUnknown, outcome)
}

func TestProvisioner_Ensure_RoleCreateNonDuplicateError_Surfaced(t *testing.T) {
	roleRepo := &errRoleRepo{createErr: errors.New("pg down")}
	p := newProvisioner(t, mem.NewUserRepository(), roleRepo, fixedUUID("x"))
	_, outcome, err := ensureForTest(p, context.Background(), stdInput())
	require.Error(t, err)
	assert.Equal(t, adminprovision.OutcomeUnknown, outcome)
	assert.ErrorContains(t, err, "ensure admin role")
}

func TestProvisioner_Ensure_RoleCreateDuplicate_Tolerated(t *testing.T) {
	// Admin role already exists (but no users assigned yet).
	roleRepo := mem.NewRoleRepository()
	role := &domain.Role{ID: auth.RoleAdmin, Name: auth.RoleAdmin}
	require.NoError(t, roleRepo.Create(context.Background(), role))

	p := newProvisioner(t, mem.NewUserRepository(), roleRepo, fixedUUID("x"))
	user, outcome, err := ensureForTest(p, context.Background(), stdInput())
	require.NoError(t, err)
	assert.Equal(t, adminprovision.OutcomeCreated, outcome)
	require.NotNil(t, user)
}

func TestProvisioner_Ensure_UserCreateInfraError_Surfaced(t *testing.T) {
	userRepo := &errUserRepo{createErr: errors.New("db down")}
	p := newProvisioner(t, userRepo, mem.NewRoleRepository(), fixedUUID("x"))
	_, outcome, err := ensureForTest(p, context.Background(), stdInput())
	require.Error(t, err)
	assert.Equal(t, adminprovision.OutcomeUnknown, outcome)
	assert.ErrorContains(t, err, "create user")
}

func TestProvisioner_Ensure_AssignToUserError_Surfaced(t *testing.T) {
	roleRepo := &errRoleRepo{assignErr: errors.New("fk violation")}
	p := newProvisioner(t, mem.NewUserRepository(), roleRepo, fixedUUID("x"))
	_, outcome, err := ensureForTest(p, context.Background(), stdInput())
	require.Error(t, err)
	assert.Equal(t, adminprovision.OutcomeUnknown, outcome)
	assert.ErrorContains(t, err, "assign admin role")
}

func TestProvisioner_Ensure_InvalidInput_Errors(t *testing.T) {
	p := newProvisioner(t, mem.NewUserRepository(), mem.NewRoleRepository(), fixedUUID("x"))
	tests := []struct {
		name string
		in   adminprovision.ProvisionInput
	}{
		{"missing hash", adminprovision.ProvisionInput{Username: "u", Email: "u@x"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, outcome, err := ensureForTest(p, context.Background(), tc.in)
			require.Error(t, err)
			assert.Equal(t, adminprovision.OutcomeUnknown, outcome)
		})
	}
}

// --- Compensate -----------------------------------------------------------

func TestProvisioner_Compensate_RemovesRoleAndUser(t *testing.T) {
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	p := newProvisioner(t, userRepo, roleRepo, fixedUUID("zzz"))
	user, _, err := ensureForTest(p, context.Background(), stdInput())
	require.NoError(t, err)
	require.NotNil(t, user)

	p.Compensate(context.Background(), user.ID)

	cnt, err := roleRepo.CountByRole(context.Background(), auth.RoleAdmin)
	require.NoError(t, err)
	assert.Equal(t, 0, cnt, "role assignment removed")
	_, err = userRepo.GetByID(context.Background(), user.ID)
	require.Error(t, err, "user row removed")
}

func TestProvisioner_Compensate_ToleratesErrorsLogOnly(t *testing.T) {
	userRepo := &errUserRepo{deleteErr: errors.New("delete failed")}
	roleRepo := &errRoleRepo{removeErr: errors.New("remove failed")}
	p := newProvisioner(t, userRepo, roleRepo, fixedUUID("x"))
	// Must not panic / return error.
	p.Compensate(context.Background(), "usr-phantom")
}

// --- test helpers ---------------------------------------------------------

func newProvisioner(
	t *testing.T,
	user ports.UserRepository, role ports.RoleRepository,
	id adminprovision.UUIDGenerator,
) *adminprovision.Provisioner {
	t.Helper()
	p, err := adminprovision.NewProvisioner(user, role, discardLogger(), id, clock.Real())
	require.NoError(t, err)
	return p
}

func seedAdmin(t *testing.T, userRepo ports.UserRepository, roleRepo ports.RoleRepository, id string) {
	t.Helper()
	u, err := domain.NewUser("seedadmin", "seed@local", "$2a$10$hash000000000000000000000000000000000000000000000000", time.Now())
	require.NoError(t, err)
	u.ID = id
	require.NoError(t, userRepo.Create(context.Background(), u))
	require.NoError(t, roleRepo.Create(context.Background(), &domain.Role{ID: auth.RoleAdmin, Name: auth.RoleAdmin}))
	_, err = roleRepo.AssignToUser(context.Background(), u.ID, auth.RoleAdmin)
	require.NoError(t, err)
}

// countingUserRepo wraps a UserRepository and counts Create calls.
type countingUserRepo struct {
	ports.UserRepository
	creates int
}

func (c *countingUserRepo) Create(ctx context.Context, u *domain.User) error {
	c.creates++
	return c.UserRepository.Create(ctx, u)
}

// duplicateUserRepo always rejects Create with ErrAuthUserDuplicate.
type duplicateUserRepo struct{}

func (r *duplicateUserRepo) Create(ctx context.Context, u *domain.User) error {
	return errcode.New(errcode.KindConflict, errcode.ErrAuthUserDuplicate, "duplicate")
}

func (r *duplicateUserRepo) GetByID(ctx context.Context, id string) (*domain.User, error) {
	return nil, errors.New("not expected on race path")
}

func (r *duplicateUserRepo) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	u, _ := domain.NewUser(username, username+"@x", "$2a$10$hashold", time.Now())
	u.ID = "usr-orphan"
	return u, nil
}
func (r *duplicateUserRepo) Update(ctx context.Context, u *domain.User) error { return nil }
func (r *duplicateUserRepo) Delete(ctx context.Context, id string) error      { return nil }

// scriptedRoleRepo returns CountByRole values from a scripted sequence; tracks
// whether AssignToUser / Create was called.
type scriptedRoleRepo struct {
	counts       []int
	i            int
	assignCalled bool
}

func (r *scriptedRoleRepo) Create(ctx context.Context, role *domain.Role) error { return nil }
func (r *scriptedRoleRepo) AssignToUser(ctx context.Context, userID, roleID string) (bool, error) {
	r.assignCalled = true
	return true, nil
}

func (r *scriptedRoleRepo) CountByRole(ctx context.Context, roleID string) (int, error) {
	if r.i >= len(r.counts) {
		return r.counts[len(r.counts)-1], nil
	}
	v := r.counts[r.i]
	r.i++
	return v, nil
}

func (r *scriptedRoleRepo) GetByUserID(ctx context.Context, userID string) ([]*domain.Role, error) {
	return nil, nil
}

func (r *scriptedRoleRepo) RemoveFromUser(ctx context.Context, userID, roleID string) error {
	return nil
}

func (r *scriptedRoleRepo) RemoveFromUserIfNotLast(ctx context.Context, userID, roleID string) (bool, error) {
	return true, nil
}

func (r *scriptedRoleRepo) GetByID(ctx context.Context, id string) (*domain.Role, error) {
	return &domain.Role{ID: id}, nil
}

func (r *scriptedRoleRepo) ListByUserID(ctx context.Context, userID string, params query.ListParams) ([]*domain.Role, error) {
	return nil, nil
}

// errRoleRepo injects errors into each method.
type errRoleRepo struct {
	createErr error
	countErr  error
	assignErr error
	removeErr error
}

func (r *errRoleRepo) Create(ctx context.Context, role *domain.Role) error { return r.createErr }
func (r *errRoleRepo) AssignToUser(ctx context.Context, userID, roleID string) (bool, error) {
	return false, r.assignErr
}

func (r *errRoleRepo) CountByRole(ctx context.Context, roleID string) (int, error) {
	return 0, r.countErr
}

func (r *errRoleRepo) GetByUserID(ctx context.Context, userID string) ([]*domain.Role, error) {
	return nil, nil
}

func (r *errRoleRepo) RemoveFromUser(ctx context.Context, userID, roleID string) error {
	return r.removeErr
}

func (r *errRoleRepo) RemoveFromUserIfNotLast(ctx context.Context, userID, roleID string) (bool, error) {
	return true, nil
}

func (r *errRoleRepo) GetByID(ctx context.Context, id string) (*domain.Role, error) {
	return &domain.Role{ID: id}, nil
}

func (r *errRoleRepo) ListByUserID(ctx context.Context, userID string, params query.ListParams) ([]*domain.Role, error) {
	return nil, nil
}

// recountErrRoleRepo returns firstCount then recountErr on subsequent CountByRole.
type recountErrRoleRepo struct {
	scriptedRoleRepo
	firstCount int
	recountErr error
	called     int
}

func (r *recountErrRoleRepo) CountByRole(ctx context.Context, roleID string) (int, error) {
	r.called++
	if r.called == 1 {
		return r.firstCount, nil
	}
	return 0, r.recountErr
}

// errUserRepo injects errors into each method.
type errUserRepo struct {
	createErr error
	deleteErr error
}

func (r *errUserRepo) Create(ctx context.Context, u *domain.User) error { return r.createErr }
func (r *errUserRepo) GetByID(ctx context.Context, id string) (*domain.User, error) {
	return nil, errors.New("not seeded")
}

func (r *errUserRepo) GetByUsername(ctx context.Context, username string) (*domain.User, error) {
	return nil, errors.New("not seeded")
}
func (r *errUserRepo) Update(ctx context.Context, u *domain.User) error { return nil }
func (r *errUserRepo) Delete(ctx context.Context, id string) error      { return r.deleteErr }
