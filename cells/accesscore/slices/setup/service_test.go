package setup_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/accesscore/internal/adminprovision"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/cells/accesscore/slices/setup"
	"github.com/ghbvf/gocell/cells/internal/testoutbox"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type stubWriter struct {
	entries []outbox.Entry
	err     error
}

func (s *stubWriter) Write(_ context.Context, e outbox.Entry) error {
	if s.err != nil {
		return s.err
	}
	s.entries = append(s.entries, e)
	return nil
}

func newService(t *testing.T, userRepo ports.UserRepository, roleRepo ports.RoleRepository, w *stubWriter) *setup.Service {
	t.Helper()
	prov, err := adminprovision.NewProvisioner(userRepo, roleRepo, discardLogger(), func() string { return "fixed-uuid" })
	require.NoError(t, err)
	opts := []setup.Option{}
	if w != nil {
		opts = append(opts, setup.WithEmitter(testoutbox.MustEmitter(t, w)))
	}
	svc, err := setup.NewService(prov, discardLogger(), opts...)
	require.NoError(t, err)
	return svc
}

// --- NewService validation ------------------------------------------------

func TestNewService_NilProvisioner_Error(t *testing.T) {
	_, err := setup.NewService(nil, discardLogger())
	require.Error(t, err)
}

func TestNewService_NilLogger_Error(t *testing.T) {
	prov, _ := adminprovision.NewProvisioner(mem.NewUserRepository(), mem.NewRoleRepository(), discardLogger(), func() string { return "x" })
	_, err := setup.NewService(prov, nil)
	require.Error(t, err)
}

// --- Status ---------------------------------------------------------------

func TestService_Status_NoAdmin_ReturnsFalse(t *testing.T) {
	svc := newService(t, mem.NewUserRepository(), mem.NewRoleRepository(), nil)
	out, err := svc.Status(context.Background())
	require.NoError(t, err)
	assert.False(t, out.HasAdmin)
}

func TestService_Status_WithAdmin_ReturnsTrue(t *testing.T) {
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	seedAdmin(t, userRepo, roleRepo)

	svc := newService(t, userRepo, roleRepo, nil)
	out, err := svc.Status(context.Background())
	require.NoError(t, err)
	assert.True(t, out.HasAdmin)
}

// --- CreateAdmin ----------------------------------------------------------

func TestService_CreateAdmin_FreshSystem_Creates_EmitsEvent(t *testing.T) {
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	w := &stubWriter{}
	svc := newService(t, userRepo, roleRepo, w)

	out, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		Username: "root",
		Email:    "root@local",
		Password: "SecretPass!23",
	})
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "root", out.Username)
	assert.Equal(t, "root@local", out.Email)
	assert.True(t, len(out.ID) > 0 && out.ID[:4] == "usr-")

	// Verify admin role assigned
	cnt, err := roleRepo.CountByRole(context.Background(), domain.RoleAdmin)
	require.NoError(t, err)
	assert.Equal(t, 1, cnt)

	// Verify event emitted
	require.Len(t, w.entries, 1, "one user.created event expected")
	assert.Equal(t, setup.TopicUserCreated, w.entries[0].EventType)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(w.entries[0].Payload, &payload))
	assert.Equal(t, out.ID, payload["user_id"])
	assert.Equal(t, "root", payload["username"])

	// Verify persisted user does NOT have PasswordResetRequired
	persisted, err := userRepo.GetByID(context.Background(), out.ID)
	require.NoError(t, err)
	assert.False(t, persisted.PasswordResetRequired, "setup path creates with operator-chosen password")
	// Verify password was hashed with bcrypt
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(persisted.PasswordHash), []byte("SecretPass!23")))
}

func TestService_CreateAdmin_OrphanRecovered_ReturnsUser_NoEmit(t *testing.T) {
	// Pre-seed a user row with the target username but no admin role assigned
	// (simulates a prior run that crashed between UserRepo.Create and
	// RoleRepo.AssignToUser). CreateAdmin must recover the orphan row, rewrite
	// the password hash, assign the admin role, and deliberately NOT emit
	// event.user.created.v1 — the event was presumably emitted by the crashed
	// run.
	userRepo := mem.NewUserRepository()
	orphan, err := domain.NewUser("root", "root@local", "$2a$10$oldhash00000000000000000000000000000000000000000000000")
	require.NoError(t, err)
	orphan.ID = "usr-orphan-prior"
	require.NoError(t, userRepo.Create(context.Background(), orphan))

	roleRepo := mem.NewRoleRepository()
	w := &stubWriter{}
	svc := newService(t, userRepo, roleRepo, w)

	out, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		Username: "root",
		Email:    "root@local",
		Password: "SecretPass!23",
	})
	require.NoError(t, err)
	require.NotNil(t, out)
	assert.Equal(t, "usr-orphan-prior", out.ID, "orphan row reused")

	// Crucially: no event.user.created.v1 emitted on OrphanRecovered.
	assert.Empty(t, w.entries, "orphan recovery must not emit duplicate event")

	// Admin role now assigned to the orphan user.
	cnt, err := roleRepo.CountByRole(context.Background(), domain.RoleAdmin)
	require.NoError(t, err)
	assert.Equal(t, 1, cnt)
}

func TestService_CreateAdmin_AlreadyExists_Returns409_NoEmit(t *testing.T) {
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	seedAdmin(t, userRepo, roleRepo)

	w := &stubWriter{}
	svc := newService(t, userRepo, roleRepo, w)

	out, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		Username: "root",
		Email:    "root@local",
		Password: "SecretPass!23",
	})
	require.Error(t, err)
	assert.Nil(t, out)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrSetupAlreadyInitialized, ec.Code)
	assert.Empty(t, w.entries, "no event on 409 path")
}

func TestService_CreateAdmin_BlankField_Returns400(t *testing.T) {
	svc := newService(t, mem.NewUserRepository(), mem.NewRoleRepository(), nil)
	tests := []struct {
		name string
		in   setup.CreateAdminInput
	}{
		{"blank username", setup.CreateAdminInput{Username: "", Email: "e@x", Password: "p"}},
		{"blank email", setup.CreateAdminInput{Username: "u", Email: "", Password: "p"}},
		{"blank password", setup.CreateAdminInput{Username: "u", Email: "e@x", Password: ""}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := svc.CreateAdmin(context.Background(), tc.in)
			require.Error(t, err)
			assert.Nil(t, out)
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec)
			assert.Equal(t, errcode.ErrAuthIdentityInvalidInput, ec.Code)
		})
	}
}

func TestService_CreateAdmin_PasswordLengthOutOfRange_Returns400(t *testing.T) {
	svc := newService(t, mem.NewUserRepository(), mem.NewRoleRepository(), nil)
	tests := []struct {
		name     string
		password string
	}{
		{"too short (7 chars)", "abc1234"},
		{"too long (129 chars)", strings.Repeat("x", 129)},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
				Username: "root",
				Email:    "root@local",
				Password: tc.password,
			})
			require.Error(t, err)
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec)
			assert.Equal(t, errcode.ErrAuthIdentityInvalidInput, ec.Code)
		})
	}
}

func TestService_CreateAdmin_EmitterFailure_Propagates(t *testing.T) {
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	w := &stubWriter{err: errors.New("broker down")}
	svc := newService(t, userRepo, roleRepo, w)

	_, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		Username: "root",
		Email:    "root@local",
		Password: "SecretPass!23",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "emit user.created")
}

func TestService_CreateAdmin_ProvisionerInfraError_Propagates(t *testing.T) {
	// RoleRepo.CountByRole error — bubbles through provisioner.Status.
	userRepo := mem.NewUserRepository()
	roleRepo := &countErrRoleRepo{err: errors.New("pg down")}
	svc := newService(t, userRepo, roleRepo, nil)

	_, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		Username: "root",
		Email:    "root@local",
		Password: "SecretPass!23",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "ensure admin")
}

// --- helpers --------------------------------------------------------------

func seedAdmin(t *testing.T, userRepo ports.UserRepository, roleRepo ports.RoleRepository) {
	t.Helper()
	u, err := domain.NewUser("existing", "existing@local", "$2a$10$stubhashXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX")
	require.NoError(t, err)
	u.ID = "usr-seed"
	require.NoError(t, userRepo.Create(context.Background(), u))
	require.NoError(t, roleRepo.Create(context.Background(), &domain.Role{ID: domain.RoleAdmin, Name: domain.RoleAdmin}))
	_, err = roleRepo.AssignToUser(context.Background(), u.ID, domain.RoleAdmin)
	require.NoError(t, err)
}

// countErrRoleRepo wraps a mem role repo but errors on CountByRole.
type countErrRoleRepo struct {
	err error
}

func (r *countErrRoleRepo) Create(_ context.Context, _ *domain.Role) error { return nil }
func (r *countErrRoleRepo) AssignToUser(_ context.Context, _, _ string) (bool, error) {
	return true, nil
}
func (r *countErrRoleRepo) CountByRole(_ context.Context, _ string) (int, error) { return 0, r.err }
func (r *countErrRoleRepo) GetByUserID(_ context.Context, _ string) ([]*domain.Role, error) {
	return nil, nil
}
func (r *countErrRoleRepo) RemoveFromUser(_ context.Context, _, _ string) error { return nil }
func (r *countErrRoleRepo) RemoveFromUserIfNotLast(_ context.Context, _, _ string) (bool, error) {
	return true, nil
}
func (r *countErrRoleRepo) GetByID(_ context.Context, _ string) (*domain.Role, error) {
	return nil, nil
}
func (r *countErrRoleRepo) ListByUserID(_ context.Context, _ string, _ query.ListParams) ([]*domain.Role, error) {
	return nil, nil
}
