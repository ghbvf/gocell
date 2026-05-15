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
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
)

type noopTxRunner struct{}

func (noopTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

var _ persistence.TxRunner = noopTxRunner{}

type setupLockTxMarkerKey struct{}

type markerTxRunner struct{}

func (markerTxRunner) RunInTx(ctx context.Context, fn func(context.Context) error) error {
	return fn(context.WithValue(ctx, setupLockTxMarkerKey{}, true))
}

var _ persistence.TxRunner = markerTxRunner{}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type stubWriter struct {
	entries []outbox.Entry
	err     error
	onWrite func()
}

func (s *stubWriter) Write(_ context.Context, e outbox.Entry) error {
	if s.err != nil {
		return s.err
	}
	if s.onWrite != nil {
		s.onWrite()
	}
	s.entries = append(s.entries, e)
	return nil
}

func newService(
	t *testing.T,
	userRepo ports.UserRepository,
	roleRepo ports.RoleRepository,
	w *stubWriter,
	extraOpts ...setup.Option,
) *setup.Service {
	t.Helper()
	prov, err := adminprovision.NewProvisioner(userRepo, roleRepo, discardLogger(), func() string {
		return "00000000-0000-4000-8000-000000000001"
	}, clock.Real())
	require.NoError(t, err)
	opts := []setup.Option{setup.WithTxManager(persistence.WrapForCell(noopTxRunner{}))}
	if w != nil {
		opts = append(opts, setup.WithEmitter(testoutbox.MustEmitter(t, w)))
	}
	opts = append(opts, extraOpts...)
	svc, err := setup.NewService(prov, discardLogger(), opts...)
	require.NoError(t, err)
	return svc
}

type recordingSetupLock struct {
	err             error
	requireTxMarker bool
	events          *[]string
	calls           int
}

func (l *recordingSetupLock) Acquire(ctx context.Context) error {
	l.calls++
	if l.requireTxMarker && ctx.Value(setupLockTxMarkerKey{}) != true {
		return errors.New("setup lock did not receive transaction context")
	}
	if l.events != nil {
		*l.events = append(*l.events, "lock")
	}
	return l.err
}

var _ ports.SetupLock = (*recordingSetupLock)(nil)

// --- NewService validation ------------------------------------------------

func TestNewService_NilProvisioner_Error(t *testing.T) {
	_, err := setup.NewService(nil, discardLogger())
	require.Error(t, err)
}

// TestNewService_NilProvisioner_ReturnsErrcode is the F3 RED test:
// provisioner nil check must return errcode (KindInvalid+ErrValidationFailed),
// not a bare fmt.Errorf.
func TestNewService_NilProvisioner_ReturnsErrcode(t *testing.T) {
	_, err := setup.NewService(nil, discardLogger())
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec, "provisioner nil check must return errcode.Error")
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
}

func TestNewService_NilLogger_Error(t *testing.T) {
	prov, _ := adminprovision.NewProvisioner(mem.NewStore(clock.Real()).UserRepository(), mem.NewStore(clock.Real()).RoleRepository(),
		discardLogger(), func() string { return "x" }, clock.Real())
	_, err := setup.NewService(prov, nil)
	require.Error(t, err)
}

// TestNewService_NilLogger_ReturnsErrcode is the F3 RED test:
// logger nil check must return errcode (KindInvalid+ErrValidationFailed),
// not a bare fmt.Errorf.
func TestNewService_NilLogger_ReturnsErrcode(t *testing.T) {
	prov, err := adminprovision.NewProvisioner(mem.NewStore(clock.Real()).UserRepository(), mem.NewStore(clock.Real()).RoleRepository(),
		discardLogger(), func() string { return "x" }, clock.Real())
	require.NoError(t, err)
	_, err = setup.NewService(prov, nil)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec, "logger nil check must return errcode.Error")
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
}

func TestNewService_TxRunnerRequired(t *testing.T) {
	prov, err := adminprovision.NewProvisioner(mem.NewStore(clock.Real()).UserRepository(), mem.NewStore(clock.Real()).RoleRepository(),
		discardLogger(), func() string { return "x" }, clock.Real())
	require.NoError(t, err)
	_, err = setup.NewService(prov, discardLogger() /* no WithTxManager */)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Contains(t, err.Error(), "TxRunner required")
}

// --- Status ---------------------------------------------------------------

func TestService_Status_NoAdmin_ReturnsFalse(t *testing.T) {
	store := mem.NewStore(clock.Real())
	svc := newService(t, store.UserRepository(), store.RoleRepository(), nil)
	out, err := svc.Status(context.Background())
	require.NoError(t, err)
	assert.False(t, out.HasAdmin)
}

func TestService_Status_WithAdmin_ReturnsTrue(t *testing.T) {
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	roleRepo := store.RoleRepository()
	seedAdmin(t, userRepo, roleRepo)

	svc := newService(t, userRepo, roleRepo, nil)
	out, err := svc.Status(context.Background())
	require.NoError(t, err)
	assert.True(t, out.HasAdmin)
}

// --- CreateAdmin ----------------------------------------------------------

func TestService_CreateAdmin_FreshSystem_Creates_EmitsEvent(t *testing.T) {
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	roleRepo := store.RoleRepository()
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
	cnt, err := roleRepo.CountByRole(context.Background(), auth.RoleAdmin)
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
	assert.False(t, persisted.PasswordResetRequired(), "setup path creates with operator-chosen password")
	// Verify password was hashed with bcrypt
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(persisted.PasswordHash), []byte("SecretPass!23")))
}

func TestService_CreateAdmin_WithSetupLock_AcquiresInsideTxBeforeEmit(t *testing.T) {
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	roleRepo := store.RoleRepository()
	events := []string{}
	w := &stubWriter{onWrite: func() { events = append(events, "emit") }}
	lock := &recordingSetupLock{requireTxMarker: true, events: &events}
	svc := newService(t, userRepo, roleRepo, w,
		setup.WithTxManager(persistence.WrapForCell(markerTxRunner{})),
		setup.WithSetupLock(lock),
	)

	out, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		Username: "root",
		Email:    "root@local",
		Password: "SecretPass!23",
	})
	require.NoError(t, err)
	require.NotNil(t, out)

	assert.Equal(t, 1, lock.calls)
	assert.Equal(t, []string{"lock", "emit"}, events,
		"setup lock must be acquired inside the transaction before user.created emit")
}

func TestService_CreateAdmin_SetupLockFailure_ShortCircuitsNoSideEffects(t *testing.T) {
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	roleRepo := store.RoleRepository()
	w := &stubWriter{}
	lockErr := errors.New("lock unavailable")
	lock := &recordingSetupLock{err: lockErr}
	svc := newService(t, userRepo, roleRepo, w, setup.WithSetupLock(lock))

	out, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		Username: "root",
		Email:    "root@local",
		Password: "SecretPass!23",
	})
	require.Error(t, err)
	assert.Nil(t, out)
	assert.ErrorIs(t, err, lockErr)
	assert.Contains(t, err.Error(), "setup: acquire setup lock")
	assert.Empty(t, w.entries, "lock failure must happen before outbox emit")

	_, userErr := userRepo.GetByUsername(context.Background(), "root")
	require.Error(t, userErr, "lock failure must happen before user creation")
	var ec *errcode.Error
	require.ErrorAs(t, userErr, &ec)
	assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
	cnt, countErr := roleRepo.CountByRole(context.Background(), auth.RoleAdmin)
	require.NoError(t, countErr)
	assert.Equal(t, 0, cnt, "lock failure must not assign admin role")
}

func TestService_CreateAdmin_NoSetupLock_StillCreates(t *testing.T) {
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	roleRepo := store.RoleRepository()
	w := &stubWriter{}
	svc := newService(t, userRepo, roleRepo, w, setup.WithSetupLock(nil))

	out, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		Username: "root",
		Email:    "root@local",
		Password: "SecretPass!23",
	})
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Len(t, w.entries, 1)
	cnt, countErr := roleRepo.CountByRole(context.Background(), auth.RoleAdmin)
	require.NoError(t, countErr)
	assert.Equal(t, 1, cnt)
}

func TestService_CreateAdmin_AlreadyExists_Returns410_NoEmit(t *testing.T) {
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	roleRepo := store.RoleRepository()
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
	store := mem.NewStore(clock.Real())
	svc := newService(t, store.UserRepository(), store.RoleRepository(), nil)
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
	store := mem.NewStore(clock.Real())
	svc := newService(t, store.UserRepository(), store.RoleRepository(), nil)
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
	store := mem.NewStore(clock.Real())
	svc := newService(t, store.UserRepository(), store.RoleRepository(), nil)
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
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	roleRepo := store.RoleRepository()
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
	userRepo := mem.NewStore(clock.Real()).UserRepository()
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
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	roleRepo := store.RoleRepository()
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
	retires := 0
	for r := range results {
		switch {
		case r.err == nil && r.out != nil:
			successes++
		case r.err != nil:
			var ec *errcode.Error
			if errors.As(r.err, &ec) && ec.Code == errcode.ErrSetupAlreadyInitialized {
				retires++
			} else {
				t.Fatalf("unexpected error: %v", r.err)
			}
		}
	}
	assert.Equal(t, 1, successes, "exactly one caller must create the admin")
	assert.Equal(t, workers-1, retires, "all other callers must see retired")

	// Final authoritative count is 1.
	cnt, err := roleRepo.CountByRole(context.Background(), auth.RoleAdmin)
	require.NoError(t, err)
	assert.Equal(t, 1, cnt)
}

// TestService_CreateAdmin_AlreadyExists_DoesNotHashPassword verifies that the
// 410 fast-path short-circuits bcrypt — previous versions hashed the password
// before checking Status, burning ~1-2s CPU per anonymous POST after admin
// already existed (round-1 M-01).
func TestService_CreateAdmin_AlreadyExists_DoesNotHashPassword(t *testing.T) {
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	roleRepo := store.RoleRepository()
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
	assert.Less(t, elapsed, testtime.SlowPoll,
		"410 fast-path must not call bcrypt")
}

// TestService_CreateAdmin_DuplicateUsername_Returns409WithoutTakeover
// pins the duplicate-username boundary: setup must return 409 without touching
// or promoting any existing user row with the same username.
func TestService_CreateAdmin_DuplicateUsername_Returns409WithoutTakeover(t *testing.T) {
	userRepo := mem.NewStore(clock.Real()).UserRepository()
	existing, err := domain.NewUser("root", "root@local", "$2a$10$oldhash00000000000000000000000000000000000000000000000", time.Now())
	require.NoError(t, err)
	existing.ID = "usr-existing-prior"
	require.NoError(t, userRepo.Create(context.Background(), existing))

	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
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

	refreshed, err := userRepo.GetByID(context.Background(), "usr-existing-prior")
	require.NoError(t, err)
	assert.Equal(t, "$2a$10$oldhash00000000000000000000000000000000000000000000000", refreshed.PasswordHash,
		"existing user hash must be untouched")
	cnt, err := roleRepo.CountByRole(context.Background(), auth.RoleAdmin)
	require.NoError(t, err)
	assert.Equal(t, 0, cnt, "duplicate username must not be promoted to admin")
}

// TestService_CreateAdmin_ControlCharInField_Returns400 pins the email/username
// control-character rejection (round-1 N-07).
func TestService_CreateAdmin_ControlCharInField_Returns400(t *testing.T) {
	store := mem.NewStore(clock.Real())
	svc := newService(t, store.UserRepository(), store.RoleRepository(), &stubWriter{})
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
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	roleRepo := store.RoleRepository()
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
	nextActionAttr, ok := ec.FindAttr("nextAction")
	require.True(t, ok)
	assert.Equal(t, "login", nextActionAttr.Value.String())

	rendered, err := json.Marshal(ec)
	require.NoError(t, err)
	assert.NotContains(t, string(rendered), "/api/",
		"details must not leak HTTP path literals; resolve via OpenAPI")
	assert.NotContains(t, string(rendered), "loginEndpoint",
		"loginEndpoint key was retired by PR-A42 — keep details minimal")
}

// --- helpers --------------------------------------------------------------

func seedAdmin(t *testing.T, userRepo ports.UserRepository, roleRepo ports.RoleRepository) {
	t.Helper()
	u, err := domain.NewUser("existing", "existing@local", "$2a$10$stubhashXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX", time.Now())
	require.NoError(t, err)
	u.ID = "usr-seed"
	require.NoError(t, userRepo.Create(context.Background(), u))
	require.NoError(t, roleRepo.Create(context.Background(), &domain.Role{ID: auth.RoleAdmin, Name: auth.RoleAdmin}))
	_, err = roleRepo.AssignToUser(context.Background(), u.ID, auth.RoleAdmin)
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
	return &domain.Role{ID: auth.RoleAdmin}, nil
}

func (r *countErrRoleRepo) ListByUserID(_ context.Context, _ string, _ query.ListParams) ([]*domain.Role, error) {
	return nil, nil
}

// CountEffectiveAdmins is the S4.0 invariant counter; setup tests exercise
// CountByRole (bootstrap idempotency) only, so this stub is intentionally
// unused.
func (r *countErrRoleRepo) CountEffectiveAdmins(_ context.Context) (int, error) {
	panic("countErrRoleRepo.CountEffectiveAdmins: unused in setup tests")
}

// EffectiveAdminExists is the S4.0 follow-up fast-path retirement check;
// these err-injection tests fail the upstream EffectiveAdminExists call
// (provisioner.Status routes through it) by making the underlying read
// surface return r.err. The provisioner.Status implementation now calls
// EffectiveAdminExists, so we route the same err through this method.
func (r *countErrRoleRepo) EffectiveAdminExists(_ context.Context) (bool, error) {
	return false, r.err
}

// newServiceWithProvisionerError builds a Service whose provisioner status
// check fails with the supplied error. Shared by service_test.go (white-box)
// and contract_test.go (envelope coverage) so the contract layer does not
// need to know which repo produces the failure.
func newServiceWithProvisionerError(t *testing.T, err error) *setup.Service {
	t.Helper()
	return newService(t, mem.NewStore(clock.Real()).UserRepository(), &countErrRoleRepo{err: err}, nil)
}

// TestService_CreateAdmin_AlreadyProvisioned_410_OperatorEnvSetIsExpected
// verifies that CreateAdmin returns 410 ErrSetupAlreadyInitialized when the
// admin was already provisioned, even though the operator Basic Auth env
// (GOCELL_BOOTSTRAP_ADMIN_*) is still set. ADR §D2 specifies these env vars
// as a persistent operator authenticator (not a one-shot seed), so their
// continued presence after admin creation is expected, not a hygiene
// concern. The service layer does not inspect env — 410 is driven by
// adminprovision.Provisioner state alone.
func TestService_CreateAdmin_AlreadyProvisioned_410_OperatorEnvSetIsExpected(t *testing.T) {
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	roleRepo := store.RoleRepository()
	seedAdmin(t, userRepo, roleRepo)

	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_USERNAME", "op")
	t.Setenv("GOCELL_BOOTSTRAP_ADMIN_PASSWORD", "opSecret123")

	w := &stubWriter{}
	svc := newService(t, userRepo, roleRepo, w)

	out, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		Username: "newadmin",
		Email:    "newadmin@local",
		Password: "SecretPass!23",
	})
	require.Error(t, err)
	assert.Nil(t, out)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrSetupAlreadyInitialized, ec.Code,
		"already provisioned must return 410 ErrSetupAlreadyInitialized")
	assert.Empty(t, w.entries, "no event emitted on 410 path")
}
