package setup_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"github.com/ghbvf/gocell/cells/accesscore/internal/adminprovision"
	"github.com/ghbvf/gocell/cells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
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
	prov, err := adminprovision.NewProvisioner(userRepo, roleRepo, discardLogger(), func() string {
		return "00000000-0000-4000-8000-000000000001"
	})
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
	_, parseErr := uuid.Parse(out.ID)
	assert.NoError(t, parseErr, "user ID must be a valid UUID")

	// Verify admin role assigned
	cnt, err := roleRepo.CountByRole(context.Background(), domain.RoleAdmin)
	require.NoError(t, err)
	assert.Equal(t, 1, cnt)

	// Verify event emitted
	require.Len(t, w.entries, 1, "one user.created event expected")
	assert.Equal(t, dto.TopicUserCreated, w.entries[0].EventType)
	var payload map[string]any
	require.NoError(t, json.Unmarshal(w.entries[0].Payload, &payload))
	assert.Equal(t, out.ID, payload["userId"])
	assert.Equal(t, "root", payload["username"])

	// Verify persisted user does NOT have PasswordResetRequired
	persisted, err := userRepo.GetByID(context.Background(), out.ID)
	require.NoError(t, err)
	assert.False(t, persisted.PasswordResetRequired, "setup path creates with operator-chosen password")
	// Verify password was hashed with bcrypt
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(persisted.PasswordHash), []byte("SecretPass!23")))
}

func TestService_CreateAdmin_OrphanRecovered_ReturnsUser_EmitsEvent(t *testing.T) {
	// Pre-seed a user row with the target username but no admin role assigned
	// (simulates a prior run that crashed between UserRepo.Create and
	// RoleRepo.AssignToUser). CreateAdmin must recover the orphan row, rewrite
	// the password hash, assign the admin role, and emit event.user.created.v1
	// because setup emits only after adminprovision.Ensure returns.
	userRepo := mem.NewUserRepository()
	orphan, err := domain.NewUser("root", "root@local", "$2a$10$oldhash00000000000000000000000000000000000000000000000")
	require.NoError(t, err)
	orphan.ID = "usr-orphan-prior"
	orphan.MarkProvisionPending(domain.UserSourceSetup)
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

	require.Len(t, w.entries, 1, "setup orphan recovery must emit user.created")
	assert.Equal(t, dto.TopicUserCreated, w.entries[0].EventType)

	// Admin role now assigned to the orphan user.
	cnt, err := roleRepo.CountByRole(context.Background(), domain.RoleAdmin)
	require.NoError(t, err)
	assert.Equal(t, 1, cnt)
}

func TestService_CreateAdmin_AlreadyExists_Returns410_NoEmit(t *testing.T) {
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
	assert.Empty(t, w.entries, "no event on 410 path")
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
		{"too long for bcrypt (73 bytes)", strings.Repeat("x", 73)},
		{"non-ASCII password would make schema chars drift from bcrypt bytes", strings.Repeat("界", 8)},
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

func TestService_CreateAdmin_FieldLengthOutOfRange_Returns400(t *testing.T) {
	svc := newService(t, mem.NewUserRepository(), mem.NewRoleRepository(), nil)
	tests := []struct {
		name string
		in   setup.CreateAdminInput
	}{
		{
			name: "username too long",
			in:   setup.CreateAdminInput{Username: strings.Repeat("u", 129), Email: "root@local", Password: "SecretPass!23"},
		},
		{
			name: "email too long",
			in:   setup.CreateAdminInput{Username: "root", Email: strings.Repeat("e", 257), Password: "SecretPass!23"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateAdmin(context.Background(), tc.in)
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
	// RoleRepo.CountByRole error — bubbles through the Status fast-path (before
	// bcrypt runs). Wrapped as "setup: status: ..." per CreateAdmin's fast-path.
	userRepo := mem.NewUserRepository()
	roleRepo := &countErrRoleRepo{err: errors.New("pg down")}
	svc := newService(t, userRepo, roleRepo, nil)

	_, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		Username: "root",
		Email:    "root@local",
		Password: "SecretPass!23",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setup: status")
	assert.Contains(t, err.Error(), "pg down")
}

// --- New in S-5: concurrent, bcrypt-skip, rollback ------------------------

// TestService_CreateAdmin_Concurrent_OnlyOneSucceeds exercises the Provisioner
// mutex: 10 goroutines all POST distinct usernames into a fresh repo; exactly
// one must return a CreateAdminOutput and the other nine must return
// ErrSetupAlreadyInitialized. This is the primary verification of the
// read-after-check atomicity fix (round-1 P0).
func TestService_CreateAdmin_Concurrent_OnlyOneSucceeds(t *testing.T) {
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	svc := newService(t, userRepo, roleRepo, &stubWriter{})

	const workers = 10
	type result struct {
		out *setup.CreateAdminOutput
		err error
	}
	results := make(chan result, workers)

	var start sync.WaitGroup
	start.Add(1)
	var done sync.WaitGroup
	done.Add(workers)
	for i := range workers {
		go func() {
			defer done.Done()
			start.Wait()
			out, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
				Username: "root" + strconv.Itoa(i),
				Email:    "root" + strconv.Itoa(i) + "@local",
				Password: "SecretPass!23",
			})
			results <- result{out: out, err: err}
		}()
	}
	start.Done()
	done.Wait()
	close(results)

	successes := 0
	retireds := 0
	for r := range results {
		switch {
		case r.err == nil && r.out != nil:
			successes++
		case r.err != nil:
			var ec *errcode.Error
			if errors.As(r.err, &ec) && ec.Code == errcode.ErrSetupAlreadyInitialized {
				retireds++
			} else {
				t.Fatalf("unexpected error: %v", r.err)
			}
		}
	}
	assert.Equal(t, 1, successes, "exactly one caller must create the admin")
	assert.Equal(t, workers-1, retireds, "all other callers must see retired")

	// Final authoritative count is 1.
	cnt, err := roleRepo.CountByRole(context.Background(), domain.RoleAdmin)
	require.NoError(t, err)
	assert.Equal(t, 1, cnt)
}

// TestService_CreateAdmin_AlreadyExists_DoesNotHashPassword verifies that the
// 410 fast-path short-circuits bcrypt — previous versions hashed the password
// before checking Status, burning ~1-2s CPU per anonymous POST after admin
// already existed (round-1 M-01).
func TestService_CreateAdmin_AlreadyExists_DoesNotHashPassword(t *testing.T) {
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	seedAdmin(t, userRepo, roleRepo)
	svc := newService(t, userRepo, roleRepo, &stubWriter{})

	start := time.Now()
	_, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		Username: "root",
		Email:    "root@local",
		Password: "SecretPass!23",
	})
	elapsed := time.Since(start)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrSetupAlreadyInitialized, ec.Code)
	// bcrypt at domain.BcryptCost (=12) takes ~200-2000ms on commodity hardware.
	// 100ms is a generous ceiling — if bcrypt ran, we'd blow past this.
	assert.Less(t, elapsed, 100*time.Millisecond,
		"410 fast-path must not call bcrypt")
}

// TestService_CreateAdmin_BootstrapPendingDuplicate_Returns409WithoutTakeover
// pins the provenance boundary: interactive setup must not reclaim a pending
// bootstrap row with the same username.
func TestService_CreateAdmin_BootstrapPendingDuplicate_Returns409WithoutTakeover(t *testing.T) {
	userRepo := mem.NewUserRepository()
	orphan, err := domain.NewUser("root", "root@local", "$2a$10$oldhash00000000000000000000000000000000000000000000000")
	require.NoError(t, err)
	orphan.ID = "usr-bootstrap-prior"
	orphan.MarkProvisionPending(domain.UserSourceBootstrap)
	orphan.MarkPasswordResetRequired()
	require.NoError(t, userRepo.Create(context.Background(), orphan))

	roleRepo := mem.NewRoleRepository()
	svc := newService(t, userRepo, roleRepo, &stubWriter{})

	out, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		Username: "root",
		Email:    "root@local",
		Password: "SecretPass!23",
	})
	require.Error(t, err)
	assert.Nil(t, out)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthUserDuplicate, ec.Code)

	refreshed, err := userRepo.GetByID(context.Background(), "usr-bootstrap-prior")
	require.NoError(t, err)
	assert.Equal(t, "$2a$10$oldhash00000000000000000000000000000000000000000000000", refreshed.PasswordHash)
	assert.True(t, refreshed.PasswordResetRequired)
	assert.Equal(t, domain.UserSourceBootstrap, refreshed.CreationSource)
	assert.Equal(t, domain.ProvisionStatePending, refreshed.ProvisionState)
	cnt, err := roleRepo.CountByRole(context.Background(), domain.RoleAdmin)
	require.NoError(t, err)
	assert.Equal(t, 0, cnt)
}

// TestService_CreateAdmin_ControlCharInField_Returns400 pins the email/username
// control-character rejection (round-1 N-07).
func TestService_CreateAdmin_ControlCharInField_Returns400(t *testing.T) {
	svc := newService(t, mem.NewUserRepository(), mem.NewRoleRepository(), &stubWriter{})
	tests := []struct {
		name string
		in   setup.CreateAdminInput
	}{
		{"newline in email", setup.CreateAdminInput{Username: "root", Email: "root@local\n", Password: "SecretPass!23"}},
		{"tab in username", setup.CreateAdminInput{Username: "ro\tot", Email: "root@local", Password: "SecretPass!23"}},
		{"cr in email", setup.CreateAdminInput{Username: "root", Email: "root\r@local", Password: "SecretPass!23"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.CreateAdmin(context.Background(), tc.in)
			require.Error(t, err)
			var ec *errcode.Error
			require.ErrorAs(t, err, &ec)
			assert.Equal(t, errcode.ErrAuthIdentityInvalidInput, ec.Code)
		})
	}
}

// TestService_CreateAdmin_AlreadyExists_DetailsContainOnlyNextAction pins the
// wire-shape contract of the 410 response: details carry a semantic
// next-action only — no HTTP path literal. Clients resolve the login endpoint
// via OpenAPI / contract registry; embedding the path here would create a
// second source of truth.
func TestService_CreateAdmin_AlreadyExists_DetailsContainOnlyNextAction(t *testing.T) {
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()
	seedAdmin(t, userRepo, roleRepo)
	svc := newService(t, userRepo, roleRepo, &stubWriter{})

	_, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		Username: "root",
		Email:    "root@local",
		Password: "SecretPass!23",
	})
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrSetupAlreadyInitialized, ec.Code)

	require.Len(t, ec.Details, 1, "details must carry exactly one key — semantic action only")
	assert.Equal(t, "login", ec.Details["nextAction"])

	rendered, err := json.Marshal(ec.Details)
	require.NoError(t, err)
	assert.NotContains(t, string(rendered), "/api/",
		"details must not leak HTTP path literals; resolve via OpenAPI")
	assert.NotContains(t, string(rendered), "loginEndpoint",
		"loginEndpoint key was retired by PR-A42 — keep details minimal")
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
	return &domain.Role{ID: domain.RoleAdmin}, nil
}
func (r *countErrRoleRepo) ListByUserID(_ context.Context, _ string, _ query.ListParams) ([]*domain.Role, error) {
	return nil, nil
}

// newServiceWithProvisionerError builds a Service whose provisioner status
// check fails with the supplied error. Shared by service_test.go (white-box)
// and contract_test.go (envelope coverage) so the contract layer does not
// need to know which repo produces the failure.
func newServiceWithProvisionerError(t *testing.T, err error) *setup.Service {
	t.Helper()
	return newService(t, mem.NewUserRepository(), &countErrRoleRepo{err: err}, nil)
}
