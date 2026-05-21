package accesscoretest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialinvalidate"
	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/slices/configreceive"
	"github.com/ghbvf/gocell/cells/accesscore/slices/identitymanage"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
	"github.com/ghbvf/gocell/kernel/persistence"
)

// BuildReceiveOption configures BuildConfigReceiveService.
type BuildReceiveOption func(*buildReceiveCfg)

type buildReceiveCfg struct {
	getter *FakeConfigGetter
}

// WithReceiveConfigGetter replaces the default FakeConfigGetter.
func WithReceiveConfigGetter(g *FakeConfigGetter) BuildReceiveOption {
	return func(c *buildReceiveCfg) { c.getter = g }
}

// BuildConfigReceiveService constructs a configreceive.Service for unit tests.
// By default it wires an empty FakeConfigGetter (all keys return ErrConfigNotFound).
func BuildConfigReceiveService(t *testing.T, opts ...BuildReceiveOption) *configreceive.Service {
	t.Helper()
	cfg := &buildReceiveCfg{getter: NewFakeConfigGetter(nil)}
	for _, o := range opts {
		o(cfg)
	}
	svc := configreceive.NewService(
		slog.Default(),
		configreceive.WithConfigGetter(cfg.getter),
	)
	return svc
}

// BuildIdentityManageOption configures BuildIdentityManageService.
type BuildIdentityManageOption func(*buildIdentityManageCfg)

type buildIdentityManageCfg struct {
	userRepo    *FakeUserRepo
	roleRepo    *FakeRoleRepo
	invalidator *credentialinvalidate.Invalidator
	clock       clock.Clock
	txMgr       persistence.CellTxManager
}

// WithIdentityManageUserRepo overrides the default FakeUserRepo.
func WithIdentityManageUserRepo(r *FakeUserRepo) BuildIdentityManageOption {
	return func(c *buildIdentityManageCfg) { c.userRepo = r }
}

// WithIdentityManageRoleRepo overrides the default FakeRoleRepo.
func WithIdentityManageRoleRepo(r *FakeRoleRepo) BuildIdentityManageOption {
	return func(c *buildIdentityManageCfg) { c.roleRepo = r }
}

// WithIdentityManageClock overrides the default clock.Real().
// Pass a *clockmock.FakeClock from tests that need deterministic time.
func WithIdentityManageClock(clk clock.Clock) BuildIdentityManageOption {
	return func(c *buildIdentityManageCfg) { c.clock = clk }
}

// WithIdentityManageTxManager overrides the default DemoCellTxManager.
func WithIdentityManageTxManager(tx persistence.CellTxManager) BuildIdentityManageOption {
	return func(c *buildIdentityManageCfg) { c.txMgr = tx }
}

// BuildIdentityManageService constructs an identitymanage.Service for unit tests,
// returning the service along with the default FakeUserRepo, FakeRoleRepo, and
// outboxtest.Recorder.
//
// The Recorder is wired as the service's outbox.Emitter so tests can assert
// which events were published.
func BuildIdentityManageService(t *testing.T, opts ...BuildIdentityManageOption) (
	*identitymanage.Service, *FakeUserRepo, *FakeRoleRepo, *outboxtest.Recorder,
) {
	t.Helper()

	cfg := &buildIdentityManageCfg{
		userRepo: NewFakeUserRepo(),
		roleRepo: NewFakeRoleRepo(),
		clock:    clock.Real(),
		txMgr:    cell.DemoCellTxManager(),
	}
	for _, o := range opts {
		o(cfg)
	}

	// Build an invalidator backed by the same FakeUserRepo so BumpAuthzEpoch
	// writes are visible to assertions on the repo.
	if cfg.invalidator == nil {
		inv, err := credentialinvalidate.New(cfg.userRepo, &noopSessionStore{}, &noopRefreshStore{})
		if err != nil {
			t.Fatalf("BuildIdentityManageService: credentialinvalidate.New: %v", err)
		}
		cfg.invalidator = inv
	}

	rec := outboxtest.NewRecorder()

	svc, err := identitymanage.NewService(
		cfg.userRepo,
		cfg.invalidator,
		slog.Default(),
		identitymanage.WithTxManager(cfg.txMgr),
		identitymanage.WithClock(cfg.clock),
		identitymanage.WithEmitter(rec),
		identitymanage.WithTokenIssuer(&noopTokenIssuer{}),
	)
	if err != nil {
		t.Fatalf("BuildIdentityManageService: identitymanage.NewService: %v", err)
	}
	return svc, cfg.userRepo, cfg.roleRepo, rec
}

// noopTokenIssuer satisfies identitymanage.TokenIssuer for tests that do not
// exercise ChangePassword. It returns an empty dto.TokenPair with no error.
type noopTokenIssuer struct{}

func (n *noopTokenIssuer) IssueForUser(_ context.Context, _ string) (dto.TokenPair, error) {
	return dto.TokenPair{}, nil
}
