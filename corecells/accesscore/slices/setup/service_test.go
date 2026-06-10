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

	"github.com/ghbvf/gocell/corecells/accesscore/internal/adminprovision"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/credential"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/domain"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
	"github.com/ghbvf/gocell/corecells/accesscore/slices/setup"
	"github.com/ghbvf/gocell/corecells/internal/testoutbox"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/redaction"
	"github.com/ghbvf/gocell/pkg/tenant"
	"github.com/ghbvf/gocell/pkg/testutil/testtime"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/credentialfence"
)

// testTenantIDStr is the string form of the canonical test tenant UUID, used
// in CreateAdminInput.TenantID (which is a string, not tenant.TenantID).
const testTenantIDStr = "00000000-0000-0000-0000-000000000001"

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
	opts := []setup.Option{
		setup.WithTxManager(persistence.WrapForCell(noopTxRunner{})),
		// Default no-op setupLock so tests that don't exercise the lock path
		// satisfy NewService's mandatory check. Tests that need to observe
		// Acquire calls override with recordingSetupLock via extraOpts.
		setup.WithSetupLock(noopSetupLock{}),
		// Low-cost hasher so the suite is not dominated by bcrypt cost-12
		// (~1.5s/hash under -race). Tests that assert bcrypt actually ran (e.g.
		// the fast-path-skip timing test) override via extraOpts with
		// credential.NewProductionHasher().
		setup.WithPasswordHasher(credential.NewTestHasher(bcrypt.MinCost)),
	}
	if w != nil {
		opts = append(opts, setup.WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, w))))
	}
	opts = append(opts, extraOpts...)
	svc, err := setup.NewService(clock.Real(), prov, discardLogger(), opts...)
	require.NoError(t, err)
	return svc
}

// noopSetupLock is the unit-test default — analogous to accesscore.NoopSetupLock
// but defined locally to avoid a setup → accesscore import cycle.
type noopSetupLock struct{}

func (noopSetupLock) Acquire(context.Context) error { return nil }

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

var _ ports.SetupLockAcquirer = (*recordingSetupLock)(nil)

// --- NewService validation ------------------------------------------------

func TestNewService_NilProvisioner_Error(t *testing.T) {
	_, err := setup.NewService(clock.Real(), nil, discardLogger())
	require.Error(t, err)
}

// TestNewService_NilProvisioner_ReturnsErrcode is the F3 RED test:
// provisioner nil check must return errcode (KindInternal+ErrCellInvalidConfig),
// not a bare fmt.Errorf. Wiring failure is operator error → 5xx, not client 4xx.
func TestNewService_NilProvisioner_ReturnsErrcode(t *testing.T) {
	_, err := setup.NewService(clock.Real(), nil, discardLogger())
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec, "provisioner nil check must return errcode.Error")
	assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
}

func TestNewService_NilLogger_Error(t *testing.T) {
	prov, _ := adminprovision.NewProvisioner(mem.NewStore(clock.Real()).UserRepository(), mem.NewStore(clock.Real()).RoleRepository(),
		discardLogger(), func() string { return "x" }, clock.Real())
	_, err := setup.NewService(clock.Real(), prov, nil)
	require.Error(t, err)
}

// TestNewService_NilLogger_ReturnsErrcode is the F3 RED test:
// logger nil check must return errcode (KindInternal+ErrCellInvalidConfig),
// not a bare fmt.Errorf. Wiring failure is operator error → 5xx, not client 4xx.
func TestNewService_NilLogger_ReturnsErrcode(t *testing.T) {
	prov, err := adminprovision.NewProvisioner(mem.NewStore(clock.Real()).UserRepository(), mem.NewStore(clock.Real()).RoleRepository(),
		discardLogger(), func() string { return "x" }, clock.Real())
	require.NoError(t, err)
	_, err = setup.NewService(clock.Real(), prov, nil)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec, "logger nil check must return errcode.Error")
	assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
}

func TestNewService_TxRunnerRequired(t *testing.T) {
	prov, err := adminprovision.NewProvisioner(mem.NewStore(clock.Real()).UserRepository(), mem.NewStore(clock.Real()).RoleRepository(),
		discardLogger(), func() string { return "x" }, clock.Real())
	require.NoError(t, err)
	_, err = setup.NewService(clock.Real(), prov, discardLogger() /* no WithTxManager */)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
	assert.Contains(t, err.Error(), "TxRunner required")
}

// --- Status ---------------------------------------------------------------

func TestService_Status_NoAdmin_ReturnsFalse(t *testing.T) {
	store := mem.NewStore(clock.Real())
	svc := newService(t, store.UserRepository(), store.RoleRepository(), nil)
	out, err := svc.Status(context.Background(), testTenantID)
	require.NoError(t, err)
	assert.False(t, out.HasAdmin)
}

func TestService_Status_WithAdmin_ReturnsTrue(t *testing.T) {
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	roleRepo := store.RoleRepository()
	seedAdmin(t, userRepo, roleRepo)

	svc := newService(t, userRepo, roleRepo, nil)
	out, err := svc.Status(context.Background(), testTenantID)
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
		TenantID: testTenantIDStr,
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
	cnt, err := roleRepo.CountByRole(context.Background(), testTenantID, auth.RoleAdmin)
	require.NoError(t, err)
	assert.Equal(t, 1, cnt)

	// Verify event emitted
	require.Len(t, w.entries, 1, "one user.created event expected")
	assert.Equal(t, dto.TopicUserCreated, w.entries[0].EventType())
	var payload map[string]any
	require.NoError(t, json.Unmarshal(w.entries[0].Payload(), &payload))
	assert.Equal(t, out.ID, payload["userId"])
	assert.Equal(t, "root", payload["username"])

	// Verify persisted user does NOT have PasswordResetRequired
	persisted, err := userRepo.GetByIDInTenant(context.Background(), testTenantID, out.ID)
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
	svc := newService(
		t, userRepo, roleRepo, w,
		setup.WithTxManager(persistence.WrapForCell(markerTxRunner{})),
		setup.WithSetupLock(lock),
	)

	out, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		TenantID: testTenantIDStr,
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
		TenantID: testTenantIDStr,
		Username: "root",
		Email:    "root@local",
		Password: "SecretPass!23",
	})
	require.Error(t, err)
	assert.Nil(t, out)
	assert.ErrorIs(t, err, lockErr)
	assert.Contains(t, err.Error(), "setup: acquire setup lock")
	assert.Empty(t, w.entries, "lock failure must happen before outbox emit")

	_, userErr := userRepo.GetByUsername(context.Background(), testTenantID, "root")
	require.Error(t, userErr, "lock failure must happen before user creation")
	var ec *errcode.Error
	require.ErrorAs(t, userErr, &ec)
	assert.Equal(t, errcode.ErrAuthUserNotFound, ec.Code)
	cnt, countErr := roleRepo.CountByRole(context.Background(), testTenantID, auth.RoleAdmin)
	require.NoError(t, countErr)
	assert.Equal(t, 0, cnt, "lock failure must not assign admin role")
}

// TestService_CreateAdmin_NilSetupLockOptionIgnored_PriorLockWins verifies that
// calling setup.WithSetupLock(nil) after a non-nil default is silently ignored.
// newService already injects noopSetupLock{} as the default; the subsequent
// WithSetupLock(nil) is not stored because the option body's
// validation.IsNilInterface check returns early. The prior default lock
// therefore remains in s.setupLock and CreateAdmin succeeds normally.
// This test does NOT cover the genuine fail-fast path (NewService rejecting a
// missing setupLock entirely) — that is covered by
// TestNewService_NilSetupLock_ReturnsErrcode.
func TestService_CreateAdmin_NilSetupLockOptionIgnored_PriorLockWins(t *testing.T) {
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	roleRepo := store.RoleRepository()
	w := &stubWriter{}
	svc := newService(t, userRepo, roleRepo, w, setup.WithSetupLock(nil))

	out, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		TenantID: testTenantIDStr,
		Username: "root",
		Email:    "root@local",
		Password: "SecretPass!23",
	})
	require.NoError(t, err)
	require.NotNil(t, out)
	require.Len(t, w.entries, 1)
	cnt, countErr := roleRepo.CountByRole(context.Background(), testTenantID, auth.RoleAdmin)
	require.NoError(t, countErr)
	assert.Equal(t, 1, cnt)
}

// TestNewService_NilSetupLock_ReturnsErrcode covers the genuine fail-fast path:
// calling setup.NewService without any WithSetupLock option (or with only a nil
// setupLock, where prior defaults are absent) must return an errcode.Error with
// ErrCellInvalidConfig code. This is operator wiring, not user input — the
// sentinel matches corecells/accesscore/cell_init.go's WithSetupLock / WithCASProtocol
// / WithBootstrapAuth fail-fast checks.
func TestNewService_NilSetupLock_ReturnsErrcode(t *testing.T) {
	prov, err := adminprovision.NewProvisioner(
		mem.NewStore(clock.Real()).UserRepository(),
		mem.NewStore(clock.Real()).RoleRepository(),
		discardLogger(),
		func() string { return "x" },
		clock.Real(),
	)
	require.NoError(t, err)
	_, err = setup.NewService(
		clock.Real(), prov, discardLogger(),
		setup.WithTxManager(persistence.WrapForCell(noopTxRunner{})),
		// No WithSetupLock — triggers the mandatory-dep fail-fast.
	)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec, "setupLock nil check must return errcode.Error")
	assert.Equal(t, errcode.ErrCellInvalidConfig, ec.Code)
}

func TestService_CreateAdmin_AlreadyExists_Returns410_NoEmit(t *testing.T) {
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	roleRepo := store.RoleRepository()
	seedAdmin(t, userRepo, roleRepo)

	w := &stubWriter{}
	svc := newService(t, userRepo, roleRepo, w)

	out, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		TenantID: testTenantIDStr,
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
		{"blank username", setup.CreateAdminInput{TenantID: testTenantIDStr, Username: "", Email: "e@x", Password: "p"}},
		{"blank email", setup.CreateAdminInput{TenantID: testTenantIDStr, Username: "u", Email: "", Password: "p"}},
		{"blank password", setup.CreateAdminInput{TenantID: testTenantIDStr, Username: "u", Email: "e@x", Password: ""}},
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
				TenantID: testTenantIDStr,
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
			in: setup.CreateAdminInput{
				TenantID: testTenantIDStr, Username: strings.Repeat("u", 129), Email: "root@local", Password: "SecretPass!23",
			},
		},
		{
			name: "email too long",
			in:   setup.CreateAdminInput{TenantID: testTenantIDStr, Username: "root", Email: strings.Repeat("e", 257), Password: "SecretPass!23"},
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
		TenantID: testTenantIDStr,
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
		TenantID: testTenantIDStr,
		Username: "root",
		Email:    "root@local",
		Password: "SecretPass!23",
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "setup: status")
	assert.Contains(t, err.Error(), "pg down")
}

// --- New in S-5: concurrent, bcrypt-skip, rollback ------------------------

// (Removed: TestService_CreateAdmin_Concurrent_OnlyOneSucceeds.) That test used
// a noopTxRunner + fixed UUID and documented a "UUID-collision race-skip" path,
// but the mem UserRepository keys uniqueness on username/email — NOT on ID — so
// the fixed UUID never collided and the race-skip path was never exercised. It
// passed only because bcrypt cost-12 timing let the first goroutine finish its
// whole Ensure (incl. role assignment) before the others reached their internal
// Status check; under the low-cost test hasher that timing margin vanishes and
// multiple admins are created. The real concurrent exactly-one guarantee comes
// from serialization (setupLock / store-paired TxRunner), which is covered by
// TestService_CreateAdmin_Concurrent_StoreTxRunner_ExactlyOneAdmin below; the
// deleted test added no coverage beyond that. The genuinely-rare
// OutcomeRaceSkipped path (PK collision under PG) is tracked for a real test in
// backlog #903.

// TestService_CreateAdmin_Concurrent_StoreTxRunner_ExactlyOneAdmin is the
// concurrency regression guard for PR #595 / B2-PROVISIONER-MUTEX-REVIEW.
//
// Problem: the mem-mode composition root in cellmodules/accesscore/module.go
// previously did NOT wire accesscore.WithTxManager(persistence.WrapForCell(
// userMemStore.TxRunner())).  Cell.Init fell through to the
// outbox.DemoCellTxManager fallback, whose RunInTx is a no-op pass-through.
// Two concurrent CreateAdmin calls both passed the CountByRole==0 fast-path
// check before either committed, producing two admins (TOCTOU; S4.0 violated).
//
// Fix: wire the Store-paired TxRunner so that memTxRunner.RunInTx holds
// store.mu for the entire closure — the CountByRole check, user write, and
// role assignment are all serialized under the same mutex.
//
// Test structure:
//   - Build service with the REAL store.TxRunner() (mutex-holding), not the
//     noopTxRunner used elsewhere in this file.
//   - Spin N goroutines all calling CreateAdmin simultaneously.
//   - Assert exactly ONE succeeds; all others get ErrSetupAlreadyInitialized.
//   - Assert the final admin count is exactly 1.
//   - Run with -race to catch data races.
//
// This test exercises the mutex-serialization path directly: the store-paired
// TxRunner holds store.mu across the whole CreateAdmin closure, so the
// CountByRole check, user write, and role assignment are serialized. That
// serialization — not any UUID-collision detection — is what guarantees
// exactly-one under concurrency (see the removal note above).
func TestService_CreateAdmin_Concurrent_StoreTxRunner_ExactlyOneAdmin(t *testing.T) {
	store := mem.NewStore(clock.Real())
	userRepo := store.UserRepository()
	roleRepo := store.RoleRepository()

	prov, err := adminprovision.NewProvisioner(
		userRepo, roleRepo, discardLogger(),
		uuid.NewString, // real UUID generator — no artificial collision
		clock.Real(),
	)
	require.NoError(t, err)

	svc, err := setup.NewService(
		clock.Real(), prov, discardLogger(),
		// Store-paired TxRunner: RunInTx holds store.mu for the entire closure.
		// This is the wiring that cellmodules/accesscore/module.go must supply
		// so that concurrent first-admin setup requests are serialized.
		setup.WithTxManager(persistence.WrapForCell(store.TxRunner())),
		setup.WithSetupLock(noopSetupLock{}),
		setup.WithPasswordHasher(credential.NewTestHasher(bcrypt.MinCost)),
	)
	require.NoError(t, err)

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
				TenantID: testTenantIDStr,
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
	retired := 0
	for r := range results {
		switch {
		case r.err == nil && r.out != nil:
			successes++
		case r.err != nil:
			var ec *errcode.Error
			require.ErrorAs(t, r.err, &ec,
				"unexpected non-errcode error: %v", r.err)
			require.Equal(t, errcode.ErrSetupAlreadyInitialized, ec.Code,
				"non-winner must return ErrSetupAlreadyInitialized, got %v", r.err)
			retired++
		}
	}
	assert.Equal(t, 1, successes, "exactly one goroutine must create the admin")
	assert.Equal(t, workers-1, retired, "all other goroutines must see ErrSetupAlreadyInitialized")

	cnt, err := roleRepo.CountByRole(context.Background(), testTenantID, auth.RoleAdmin)
	require.NoError(t, err)
	assert.Equal(t, 1, cnt, "final admin count must be exactly 1 (S4.0 invariant)")
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
	// Production-cost hasher on purpose: this test proves the 410 fast-path
	// short-circuits hashing by asserting elapsed < SlowPoll. With a low-cost
	// hasher even an un-skipped hash would beat the ceiling, defeating the test.
	svc := newService(t, userRepo, roleRepo, &stubWriter{},
		setup.WithPasswordHasher(credential.NewProductionHasher()))

	start := time.Now()
	_, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		TenantID: testTenantIDStr,
		Username: "root",
		Email:    "root@local",
		Password: "SecretPass!23",
	})
	elapsed := time.Since(start)
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrSetupAlreadyInitialized, ec.Code)
	// bcrypt at credential.ProductionCost (=12) takes ~200-2000ms on commodity
	// hardware. 100ms is a generous ceiling — if bcrypt ran, we'd blow past this.
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
	require.NoError(t, userRepo.Create(context.Background(), testTenantID, existing))

	roleRepo := mem.NewStore(clock.Real()).RoleRepository()
	svc := newService(t, userRepo, roleRepo, &stubWriter{})

	out, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		TenantID: testTenantIDStr,
		Username: "root",
		Email:    "root@local",
		Password: "SecretPass!23",
	})
	require.Error(t, err)
	assert.Nil(t, out)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrAuthUserDuplicate, ec.Code)

	refreshed, err := userRepo.GetByIDInTenant(context.Background(), testTenantID, "usr-existing-prior")
	require.NoError(t, err)
	assert.Equal(t, "$2a$10$oldhash00000000000000000000000000000000000000000000000", refreshed.PasswordHash,
		"existing user hash must be untouched")
	cnt, err := roleRepo.CountByRole(context.Background(), testTenantID, auth.RoleAdmin)
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
		{"newline in email", setup.CreateAdminInput{
			TenantID: testTenantIDStr, Username: "root", Email: "root@local\n", Password: "SecretPass!23",
		}},
		{"tab in username", setup.CreateAdminInput{
			TenantID: testTenantIDStr, Username: "ro\tot", Email: "root@local", Password: "SecretPass!23",
		}},
		{"cr in email", setup.CreateAdminInput{TenantID: testTenantIDStr, Username: "root", Email: "root\r@local", Password: "SecretPass!23"}},
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
		TenantID: testTenantIDStr,
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
	assert.Equal(t, "login", nextActionAttr.Value().(string))

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
	require.NoError(t, userRepo.Create(context.Background(), testTenantID, u))
	require.NoError(t, roleRepo.Create(context.Background(), testTenantID, &domain.Role{ID: auth.RoleAdmin, Name: auth.RoleAdmin}))
	_, err = roleRepo.AssignToUser(context.Background(), testTenantID, u.ID, auth.RoleAdmin)
	require.NoError(t, err)
}

// countErrRoleRepo wraps a mem role repo but errors on CountByRole.
type countErrRoleRepo struct {
	err error
}

func (r *countErrRoleRepo) Create(_ context.Context, _ tenant.TenantID, _ *domain.Role) error {
	return nil
}

func (r *countErrRoleRepo) AssignToUser(_ context.Context, _ tenant.TenantID, _, _ string) (bool, error) {
	return true, nil
}

func (r *countErrRoleRepo) CountByRole(_ context.Context, _ tenant.TenantID, _ string) (int, error) {
	return 0, r.err
}

func (r *countErrRoleRepo) GetByUserID(_ context.Context, _ tenant.TenantID, _ string) ([]*domain.Role, error) {
	return nil, nil
}

func (r *countErrRoleRepo) RemoveFromUser(_ context.Context, _ tenant.TenantID, _, _ string) error {
	return nil
}

func (r *countErrRoleRepo) RemoveFromUserIfNotLast(_ context.Context, _ tenant.TenantID, _, _ string) (bool, error) {
	return true, nil
}

func (r *countErrRoleRepo) GetByID(_ context.Context, _ tenant.TenantID, _ string) (*domain.Role, error) {
	return &domain.Role{ID: auth.RoleAdmin}, nil
}

func (r *countErrRoleRepo) ListByUserID(_ context.Context, _ tenant.TenantID, _ string, _ query.ListParams) ([]*domain.Role, error) {
	return nil, nil
}

// CountEffectiveAdmins is the S4.0 invariant counter; setup tests exercise
// CountByRole (bootstrap idempotency) only, so this stub is intentionally
// unused.
func (r *countErrRoleRepo) CountEffectiveAdmins(_ context.Context, _ tenant.TenantID) (int, error) {
	panic("countErrRoleRepo.CountEffectiveAdmins: unused in setup tests")
}

// EffectiveAdminExists is the S4.0 follow-up fast-path retirement check;
// these err-injection tests fail the upstream EffectiveAdminExists call
// (provisioner.Status routes through it) by making the underlying read
// surface return r.err. The provisioner.Status implementation now calls
// EffectiveAdminExists, so we route the same err through this method.
func (r *countErrRoleRepo) EffectiveAdminExists(_ context.Context, _ tenant.TenantID) (bool, error) {
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
		TenantID: testTenantIDStr,
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

// TestService_CreateAdmin_IsRLSScoped asserts that CreateAdmin wraps the repo
// calls in a scoped transaction so the RLS tenant_isolation policy on users /
// roles / role_assignments is satisfied (PR-3b Site 3 — setup RLS scope).
//
// A scopeCapturingUserRepo intercepts the first Create call and records
// whether tenant.ScopeFromContext was set in the context.
// outbox.DemoCellTxManager is a pass-through that preserves WithScope values
// (same approach as TestHasRole_IsRLSScoped in rbaccheck).
func TestService_CreateAdmin_IsRLSScoped(t *testing.T) {
	inner := mem.NewStore(clock.Real())
	cap := &scopeCapturingUserRepo{inner: inner.UserRepository()}
	roleRepo := inner.RoleRepository()

	svc := newService(t, cap, roleRepo, nil,
		setup.WithTxManager(outbox.DemoCellTxManager()),
	)

	_, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		TenantID: testTenantIDStr,
		Username: "root",
		Email:    "root@local",
		Password: "SecretPass!23",
	})
	require.NoError(t, err)

	assert.True(t, cap.capturedOK,
		"Create must run inside a scoped tx (tenant.ScopeFromContext must be set)")
	assert.Equal(t, testTenantID, cap.capturedScope,
		"Create scope must equal the request tenant")
}

// scopeCapturingUserRepo wraps a UserRepository and records whether the
// context passed to Create carries a tenant scope.
type scopeCapturingUserRepo struct {
	inner         ports.UserRepository
	capturedScope tenant.TenantID
	capturedOK    bool
}

var _ ports.UserRepository = (*scopeCapturingUserRepo)(nil)

func (r *scopeCapturingUserRepo) Create(ctx context.Context, t tenant.TenantID, u *domain.User) error {
	r.capturedScope, r.capturedOK = tenant.ScopeFromContext(ctx)
	return r.inner.Create(ctx, t, u)
}

func (r *scopeCapturingUserRepo) GetByIDInTenant(ctx context.Context, t tenant.TenantID, id string) (*domain.User, error) {
	return r.inner.GetByIDInTenant(ctx, t, id)
}

func (r *scopeCapturingUserRepo) GetByUsername(ctx context.Context, t tenant.TenantID, username string) (*domain.User, error) {
	return r.inner.GetByUsername(ctx, t, username)
}

func (r *scopeCapturingUserRepo) Delete(ctx context.Context, t tenant.TenantID, id string) error {
	return r.inner.Delete(ctx, t, id)
}

func (r *scopeCapturingUserRepo) UpdateProfile(ctx context.Context, t tenant.TenantID, userID string, name, email *domain.NonEmpty, now time.Time) (*domain.User, error) { //nolint:lll // test stub matching interface signature
	return r.inner.UpdateProfile(ctx, t, userID, name, email, now)
}

func (r *scopeCapturingUserRepo) UpdateLockState(ctx context.Context, t tenant.TenantID, userID string, status domain.UserStatus, now time.Time) error { //nolint:lll // test stub matching interface signature
	return r.inner.UpdateLockState(ctx, t, userID, status, now)
}

func (r *scopeCapturingUserRepo) UpdatePasswordResetFlag(ctx context.Context, t tenant.TenantID, userID string, required bool, now time.Time) error { //nolint:lll // test stub matching interface signature
	return r.inner.UpdatePasswordResetFlag(ctx, t, userID, required, now)
}

func (r *scopeCapturingUserRepo) UpdatePassword(ctx context.Context, t tenant.TenantID, userID, newHash string, resetRequired bool, expectedPasswordVersion int64) (int64, error) { //nolint:lll // test stub matching interface signature
	return r.inner.UpdatePassword(ctx, t, userID, newHash, resetRequired, expectedPasswordVersion)
}

func (r *scopeCapturingUserRepo) BumpAuthzEpoch(ctx context.Context, t tenant.TenantID, userID string, tok credentialfence.FenceToken) (int64, error) { //nolint:lll // test stub matching interface signature
	return r.inner.BumpAuthzEpoch(ctx, t, userID, tok)
}

func (r *scopeCapturingUserRepo) GetByIDForUpdate(ctx context.Context, t tenant.TenantID, id string) (*domain.User, error) {
	return r.inner.GetByIDForUpdate(ctx, t, id)
}

func (r *scopeCapturingUserRepo) GetByUsernameForUpdate(ctx context.Context, t tenant.TenantID, username string) (*domain.User, error) {
	return r.inner.GetByUsernameForUpdate(ctx, t, username)
}

func (r *scopeCapturingUserRepo) UpdateLockoutFields(ctx context.Context, t tenant.TenantID, u *domain.User) error {
	return r.inner.UpdateLockoutFields(ctx, t, u)
}

// scopeCapturingRoleRepo records the tenant scope observed on every
// EffectiveAdminExists call (the admin-existence probe that provisioner.Status
// runs). Embedding ports.RoleRepository satisfies the unobserved methods.
type scopeCapturingRoleRepo struct {
	ports.RoleRepository
	scopes []scopeObservation
}

type scopeObservation struct {
	scope tenant.TenantID
	ok    bool
}

func (r *scopeCapturingRoleRepo) EffectiveAdminExists(ctx context.Context, t tenant.TenantID) (bool, error) {
	s, ok := tenant.ScopeFromContext(ctx)
	r.scopes = append(r.scopes, scopeObservation{scope: s, ok: ok})
	return r.RoleRepository.EffectiveAdminExists(ctx, t)
}

// assertAllScoped fails unless at least one EffectiveAdminExists call was
// observed and EVERY observation carried the expected tenant scope.
func (r *scopeCapturingRoleRepo) assertAllScoped(t *testing.T, want tenant.TenantID) {
	t.Helper()
	require.NotEmpty(t, r.scopes, "EffectiveAdminExists was never called — scope assertion is vacuous")
	for i, obs := range r.scopes {
		assert.True(t, obs.ok, "EffectiveAdminExists call #%d ran without a tenant scope (RLS GUC would be unset)", i)
		assert.Equal(t, want, obs.scope, "EffectiveAdminExists call #%d scoped to the wrong tenant", i)
	}
}

// TestService_Status_IsRLSScoped (PR-3b review F1, Site 2) asserts that the
// public GET /setup/status admin-existence read runs inside a tenant-scoped tx,
// so under the restricted app-serving pool (#1676) EffectiveAdminExists is not
// fail-closed to 0 rows by an unset app.tenant_id GUC (which would falsely
// report hasAdmin:false). DemoCellTxManager is a pass-through that preserves the
// WithScope value scopedtx.Do sets.
func TestService_Status_IsRLSScoped(t *testing.T) {
	inner := mem.NewStore(clock.Real())
	roleCap := &scopeCapturingRoleRepo{RoleRepository: inner.RoleRepository()}
	svc := newService(t, inner.UserRepository(), roleCap, nil,
		setup.WithTxManager(outbox.DemoCellTxManager()),
	)

	_, err := svc.Status(context.Background(), testTenantID)
	require.NoError(t, err)
	roleCap.assertAllScoped(t, testTenantID)
}

// TestService_CreateAdmin_FastPathStatus_IsRLSScoped (PR-3b review F1, Site 3)
// asserts that the pre-bcrypt fast-path admin-existence check in CreateAdmin
// reads role_assignments inside a scoped tx. It runs BEFORE the write-tx
// scopedtx.Do, so it needs its own scope — otherwise the restricted pool (#1676)
// fail-closes it to 0 rows and every flood request falls through to bcrypt.
func TestService_CreateAdmin_FastPathStatus_IsRLSScoped(t *testing.T) {
	inner := mem.NewStore(clock.Real())
	roleCap := &scopeCapturingRoleRepo{RoleRepository: inner.RoleRepository()}
	svc := newService(t, inner.UserRepository(), roleCap, nil,
		setup.WithTxManager(outbox.DemoCellTxManager()),
	)

	// Fresh store has no admin → the fast-path EffectiveAdminExists check runs
	// (and so does the in-tx Ensure check); assertAllScoped proves BOTH are scoped.
	_, err := svc.CreateAdmin(context.Background(), setup.CreateAdminInput{
		TenantID: testTenantIDStr, Username: "root", Email: "root@local", Password: "SecretPass!23",
	})
	require.NoError(t, err)
	roleCap.assertAllScoped(t, testTenantID)
}

// --- RecordBootstrapAuthFail ------------------------------------------------

// bootstrapTestIPSalt is the keyed-hash salt used by RecordBootstrapAuthFail
// tests; the composition root hashes the IP before calling the service.
var bootstrapTestIPSalt = []byte("test-ip-hash-salt-32-bytes-pad!!")

func TestService_RecordBootstrapAuthFail_ValidReasons(t *testing.T) {
	t.Parallel()
	tests := []struct {
		reason   string
		clientIP string
	}{
		{reason: "missing_header", clientIP: "192.0.2.1"},
		{reason: "wrong_credentials", clientIP: "10.0.0.1"},
		{reason: "rate_limited", clientIP: ""},
	}
	for _, tc := range tests {
		tc := tc
		t.Run(tc.reason, func(t *testing.T) {
			t.Parallel()
			store := mem.NewStore(clock.Real())
			w := &stubWriter{}
			svc := newService(t, store.UserRepository(), store.RoleRepository(), w)

			hash := redaction.HashIP(bootstrapTestIPSalt, tc.clientIP)
			err := svc.RecordBootstrapAuthFail(context.Background(), tc.reason, hash)
			require.NoError(t, err)
			require.Len(t, w.entries, 1, "exactly one outbox entry emitted")

			raw := w.entries[0].Payload()
			// The wire payload carries the keyed hash, never the plaintext IP
			// (#1488). Decode into a string-bearing view — the producer DTO field
			// is the sealed redaction.IPHash, which has no UnmarshalJSON.
			var payload struct {
				Reason       string `json:"reason"`
				ClientIPHash string `json:"clientIpHash"`
			}
			require.NoError(t, json.Unmarshal(raw, &payload))
			assert.Equal(t, tc.reason, payload.Reason)
			assert.Equal(t, hash.String(), payload.ClientIPHash)
			assert.Equal(t, dto.TopicBootstrapAuthFailed, w.entries[0].EventType())
			if tc.clientIP != "" {
				assert.NotContains(t, string(raw), tc.clientIP,
					"plaintext client IP must not appear in the replayable payload")
			}
		})
	}
}

func TestService_RecordBootstrapAuthFail_InvalidReason_Error(t *testing.T) {
	t.Parallel()
	store := mem.NewStore(clock.Real())
	w := &stubWriter{}
	svc := newService(t, store.UserRepository(), store.RoleRepository(), w)

	err := svc.RecordBootstrapAuthFail(context.Background(), "invalid_reason", redaction.HashIP(bootstrapTestIPSalt, "1.2.3.4"))
	require.Error(t, err)
	var ec *errcode.Error
	require.ErrorAs(t, err, &ec)
	assert.Equal(t, errcode.ErrValidationFailed, ec.Code)
	assert.Empty(t, w.entries, "no outbox entry on invalid reason")
}

func TestService_RecordBootstrapAuthFail_EmitterFailure_Propagates(t *testing.T) {
	t.Parallel()
	store := mem.NewStore(clock.Real())
	w := &stubWriter{err: errors.New("broker down")}
	svc := newService(t, store.UserRepository(), store.RoleRepository(), w)

	err := svc.RecordBootstrapAuthFail(context.Background(), "rate_limited", redaction.HashIP(bootstrapTestIPSalt, "1.2.3.4"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "broker down")
}

// TestService_RecordBootstrapAuthFail_EmitsInsideTx mirrors
// TestService_CreateAdmin_WithSetupLock_AcquiresInsideTxBeforeEmit and verifies
// that RecordBootstrapAuthFail calls outbox.Emit INSIDE txRunner.RunInTx.
// A markerTxRunner tags the context; the stubWriter asserts the tag is present
// when the emit is received.
func TestService_RecordBootstrapAuthFail_EmitsInsideTx(t *testing.T) {
	store := mem.NewStore(clock.Real())
	var emitHappenedInsideTx bool
	w := &stubWriter{onWrite: func() {
		// This callback runs when outbox.Write is called; the ctx value propagated
		// by markerTxRunner must be present on the context flowing through the tx.
		emitHappenedInsideTx = true
	}}
	// txMarkerWriter wraps stubWriter and checks the tx context marker at write time.
	txChecked := false
	txWriter := &txCheckWriter{inner: w, key: setupLockTxMarkerKey{}, onCheck: func(insideTx bool) {
		txChecked = true
		emitHappenedInsideTx = insideTx
	}}
	svc := newService(t, store.UserRepository(), store.RoleRepository(), nil,
		setup.WithTxManager(persistence.WrapForCell(markerTxRunner{})),
		setup.WithEmitter(outbox.WrapEmitterForCell(testoutbox.MustEmitter(t, txWriter))),
	)

	err := svc.RecordBootstrapAuthFail(context.Background(), "rate_limited", redaction.HashIP(bootstrapTestIPSalt, "1.2.3.4"))
	require.NoError(t, err)
	assert.True(t, txChecked, "txCheckWriter must be invoked")
	assert.True(t, emitHappenedInsideTx,
		"outbox.Emit must be called inside txRunner.RunInTx for RecordBootstrapAuthFail")
}

// txCheckWriter is a write-once outbox.Writer that validates the tx context
// marker at write time.  key is the context key injected by markerTxRunner;
// onCheck is called with true when the key is present, false otherwise.
type txCheckWriter struct {
	inner   *stubWriter
	key     interface{}
	onCheck func(bool)
}

func (w *txCheckWriter) Write(ctx context.Context, e outbox.Entry) error {
	if w.onCheck != nil {
		w.onCheck(ctx.Value(w.key) == true)
	}
	return w.inner.Write(ctx, e)
}
