//go:build unix

package accesscore

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/ghbvf/gocell/cells/access-core/internal/domain"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/runtime/worker"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixedReaderForCell is a simple io.Reader that returns deterministic bytes.
// Defined here because fixedReader in bootstrap_test.go lives in a different
// package (initialadmin).
type fixedReaderForCell struct{ data []byte }

func (r *fixedReaderForCell) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = r.data[i%len(r.data)]
	}
	return len(p), nil
}

// newCellFixedSource returns a deterministic password source for cell-level tests.
func newCellFixedSource() *fixedReaderForCell {
	b := make([]byte, 32)
	for i := range b {
		b[i] = byte('A' + (i % 26))
	}
	return &fixedReaderForCell{data: b}
}

// newTestCellWithBootstrap constructs a fully wired AccessCore using mem repos
// and the provided bootstrap options. The sink parameter (if non-nil) is wired
// via WithBootstrapWorkerSink.
func newTestCellWithBootstrap(
	t *testing.T,
	bootstrapOpts []InitialAdminOption,
	sink func(worker.Worker),
) *AccessCore {
	t.Helper()
	opts := []Option{
		WithUserRepository(mem.NewUserRepository()),
		WithSessionRepository(mem.NewSessionRepository()),
		WithRoleRepository(mem.NewRoleRepository()),
		WithPublisher(noopPublisher{}),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
	}
	if len(bootstrapOpts) > 0 {
		opts = append(opts, WithInitialAdminBootstrap(bootstrapOpts...))
	}
	if sink != nil {
		opts = append(opts, WithBootstrapWorkerSink(sink))
	}
	return NewAccessCore(opts...)
}

func testDeps() cell.Dependencies {
	return cell.Dependencies{
		Config:         make(map[string]any),
		DurabilityMode: cell.DurabilityDemo,
	}
}

// TestInit_WithInitialAdminBootstrap_RegistersCleanerViaSink verifies that when
// no admin exists, bootstrap runs successfully and the sink receives a non-nil
// worker.
func TestInit_WithInitialAdminBootstrap_RegistersCleanerViaSink(t *testing.T) {
	dir := t.TempDir()

	var receivedWorkers []worker.Worker
	sink := func(w worker.Worker) { receivedWorkers = append(receivedWorkers, w) }

	bootstrapOpts := []InitialAdminOption{
		WithBootstrapCredentialPath(filepath.Join(dir, "initial_admin_password")),
		WithBootstrapTTL(time.Hour),
		withBootstrapPasswordSource(newCellFixedSource()),
	}

	c := newTestCellWithBootstrap(t, bootstrapOpts, sink)
	require.NoError(t, c.Init(context.Background(), testDeps()))

	require.Len(t, receivedWorkers, 1, "sink must receive exactly one cleaner worker")
	assert.NotNil(t, receivedWorkers[0])
}

// TestInit_BootstrapMissingSink_FailsFast verifies that Init returns an error
// when WithInitialAdminBootstrap is set without WithBootstrapWorkerSink.
func TestInit_BootstrapMissingSink_FailsFast(t *testing.T) {
	dir := t.TempDir()
	bootstrapOpts := []InitialAdminOption{
		WithBootstrapCredentialPath(filepath.Join(dir, "initial_admin_password")),
		WithBootstrapTTL(time.Hour),
		withBootstrapPasswordSource(newCellFixedSource()),
	}
	// No sink provided.
	c := newTestCellWithBootstrap(t, bootstrapOpts, nil)
	err := c.Init(context.Background(), testDeps())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "WithBootstrapWorkerSink")
}

// TestInit_BootstrapDefaultBehaviorIsNoop verifies that when
// WithInitialAdminBootstrap is NOT set, Init succeeds without creating any
// user and without calling the sink.
func TestInit_BootstrapDefaultBehaviorIsNoop(t *testing.T) {
	userRepo := mem.NewUserRepository()
	var sinkCalled bool

	c := NewAccessCore(
		WithUserRepository(userRepo),
		WithSessionRepository(mem.NewSessionRepository()),
		WithRoleRepository(mem.NewRoleRepository()),
		WithPublisher(noopPublisher{}),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithBootstrapWorkerSink(func(w worker.Worker) { sinkCalled = true }),
	)
	require.NoError(t, c.Init(context.Background(), testDeps()))

	assert.False(t, sinkCalled, "sink must not be called when bootstrap is not configured")

	// No users should exist in the repo.
	_, err := userRepo.GetByUsername(context.Background(), "admin")
	assert.Error(t, err, "no user should be created when bootstrap is not configured")
}

// TestInit_BootstrapAlreadyHasAdmin_NilCleaner verifies that when an admin
// already exists, bootstrap is a no-op and the sink is not called.
func TestInit_BootstrapAlreadyHasAdmin_NilCleaner(t *testing.T) {
	dir := t.TempDir()
	userRepo := mem.NewUserRepository()
	roleRepo := mem.NewRoleRepository()

	// Pre-seed an admin.
	require.NoError(t, roleRepo.Create(context.Background(), &domain.Role{
		ID: domain.RoleAdmin, Name: domain.RoleAdmin,
	}))
	adminUser, err := domain.NewUser("admin", "admin@gocell.local", "$2a$12$testhash")
	require.NoError(t, err)
	adminUser.ID = "usr-preexisting-admin"
	require.NoError(t, userRepo.Create(context.Background(), adminUser))
	_, err = roleRepo.AssignToUser(context.Background(), adminUser.ID, domain.RoleAdmin)
	require.NoError(t, err)

	var sinkCalled bool
	sink := func(w worker.Worker) { sinkCalled = true }

	bootstrapOpts := []InitialAdminOption{
		WithBootstrapCredentialPath(filepath.Join(dir, "initial_admin_password")),
		WithBootstrapTTL(time.Hour),
		withBootstrapPasswordSource(newCellFixedSource()),
	}

	c := NewAccessCore(
		WithUserRepository(userRepo),
		WithSessionRepository(mem.NewSessionRepository()),
		WithRoleRepository(roleRepo),
		WithPublisher(noopPublisher{}),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithInitialAdminBootstrap(bootstrapOpts...),
		WithBootstrapWorkerSink(sink),
	)
	require.NoError(t, c.Init(context.Background(), testDeps()))

	assert.False(t, sinkCalled, "sink must not be called when admin already exists (bootstrap silent skip)")
}

// TestInit_BootstrapUser_HasPasswordResetRequired verifies that the bootstrap
// user has PasswordResetRequired=true after Init.
func TestInit_BootstrapUser_HasPasswordResetRequired(t *testing.T) {
	dir := t.TempDir()
	userRepo := mem.NewUserRepository()

	bootstrapOpts := []InitialAdminOption{
		WithBootstrapCredentialPath(filepath.Join(dir, "initial_admin_password")),
		WithBootstrapTTL(time.Hour),
		withBootstrapPasswordSource(newCellFixedSource()),
	}

	var receivedWorker worker.Worker
	c := NewAccessCore(
		WithUserRepository(userRepo),
		WithSessionRepository(mem.NewSessionRepository()),
		WithRoleRepository(mem.NewRoleRepository()),
		WithPublisher(noopPublisher{}),
		WithJWTIssuer(testIssuer),
		WithJWTVerifier(testVerifier),
		WithInitialAdminBootstrap(bootstrapOpts...),
		WithBootstrapWorkerSink(func(w worker.Worker) { receivedWorker = w }),
	)
	require.NoError(t, c.Init(context.Background(), testDeps()))
	assert.NotNil(t, receivedWorker)

	user, err := userRepo.GetByUsername(context.Background(), "admin")
	require.NoError(t, err)
	assert.True(t, user.PasswordResetRequired, "bootstrap user must have PasswordResetRequired=true")
}

// outbox import is already available via noopPublisher in cell_test.go (same package).
// Verify the noopPublisher is accessible.
var _ outbox.Publisher = noopPublisher{}
