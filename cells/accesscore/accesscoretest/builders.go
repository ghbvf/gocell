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
	"github.com/ghbvf/gocell/pkg/errcode"
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
		slog.New(slog.DiscardHandler),
		configreceive.WithConfigGetter(cfg.getter),
	)
	return svc
}

// BuildIdentityManageOption configures BuildIdentityManageService.
type BuildIdentityManageOption func(*buildIdentityManageCfg)

type buildIdentityManageCfg struct {
	userRepo    *FakeUserRepo
	invalidator *credentialinvalidate.Invalidator
	clock       clock.Clock
	txMgr       persistence.CellTxManager
}

// WithIdentityManageUserRepo overrides the default FakeUserRepo.
func WithIdentityManageUserRepo(r *FakeUserRepo) BuildIdentityManageOption {
	return func(c *buildIdentityManageCfg) { c.userRepo = r }
}

// WithIdentityManageClock overrides the default clock.Real().
// Pass a clock.Clock implementation that returns deterministic time,
// e.g. kernel/clock/clockmock.New(...) from your _test.go.
func WithIdentityManageClock(clk clock.Clock) BuildIdentityManageOption {
	return func(c *buildIdentityManageCfg) { c.clock = clk }
}

// WithIdentityManageTxManager overrides the default DemoCellTxManager.
func WithIdentityManageTxManager(tx persistence.CellTxManager) BuildIdentityManageOption {
	return func(c *buildIdentityManageCfg) { c.txMgr = tx }
}

// BuildIdentityManageService constructs an identitymanage.Service for unit tests,
// returning the service along with the default FakeUserRepo and
// outboxtest.Recorder.
//
// The Recorder is wired as the service's outbox.Emitter so tests can assert
// which events were published.
//
// FakeRoleRepo is not included in the return tuple because identitymanage.Service
// does not depend on RoleRepository by default. To enable last-admin protection,
// inject a FakeRoleRepo via identitymanage.WithLastAdminProtection in a custom
// build.
func BuildIdentityManageService(t *testing.T, opts ...BuildIdentityManageOption) (
	*identitymanage.Service, *FakeUserRepo, *outboxtest.Recorder,
) {
	t.Helper()

	cfg := &buildIdentityManageCfg{
		userRepo: NewFakeUserRepo(),
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
		slog.New(slog.DiscardHandler),
		identitymanage.WithTxManager(cfg.txMgr),
		identitymanage.WithClock(cfg.clock),
		identitymanage.WithEmitter(rec),
		identitymanage.WithTokenIssuer(&noopTokenIssuer{}),
	)
	if err != nil {
		t.Fatalf("BuildIdentityManageService: identitymanage.NewService: %v", err)
	}
	return svc, cfg.userRepo, rec
}

// noopTokenIssuer satisfies identitymanage.TokenIssuer for tests that do not
// exercise ChangePassword. It returns an error immediately to prevent silent
// test passes when token issuance is unexpectedly reached; wire a real
// TokenIssuer via BuildIdentityManageService options for ChangePassword tests.
type noopTokenIssuer struct{}

func (n *noopTokenIssuer) IssueForUser(_ context.Context, _ string) (dto.TokenPair, error) {
	return dto.TokenPair{}, errcode.New(errcode.KindInternal, errcode.ErrNotImplemented,
		"noopTokenIssuer.IssueForUser not implemented — wire a real TokenIssuer for ChangePassword tests")
}
