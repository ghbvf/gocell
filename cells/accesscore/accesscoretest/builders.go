package accesscoretest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/slices/configreceive"
	"github.com/ghbvf/gocell/cells/accesscore/slices/identitymanage"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/outbox/outboxtest"
)

// stubTokenIssuer is a minimal identitymanage.TokenIssuer that returns a zero
// TokenPair. Used by BuildIdentityManageService for paths that do not exercise
// ChangePassword. Callers that need real token issuance should inject a real
// sessionlogin.Service via WithIdentityTokenIssuer.
type stubTokenIssuer struct{}

func (stubTokenIssuer) IssueForUser(_ context.Context, _ string) (dto.TokenPair, error) {
	return dto.TokenPair{}, nil
}

// Compile-time: stubTokenIssuer must satisfy the narrow identitymanage.TokenIssuer
// interface.
var _ identitymanage.TokenIssuer = stubTokenIssuer{}

// BuildIdentityManageOption configures BuildIdentityManageService.
type BuildIdentityManageOption func(*buildIdentityConfig)

type buildIdentityConfig struct {
	fixture *AccessFixture
	clock   clock.Clock
	logger  *slog.Logger
	issuer  identitymanage.TokenIssuer
}

// WithIdentityFixture injects a pre-constructed (and possibly pre-seeded)
// AccessFixture into the builder. When not set, the builder creates a fresh
// one backed by clock.Real(). Use this option when you need to inspect or
// pre-seed state before calling BuildIdentityManageService.
func WithIdentityFixture(f *AccessFixture) BuildIdentityManageOption {
	return func(c *buildIdentityConfig) {
		if f != nil {
			c.fixture = f
		}
	}
}

// WithIdentityClock injects a clock (e.g. a test clock) into the identity
// service and its underlying invalidator. Defaults to clock.Real().
func WithIdentityClock(clk clock.Clock) BuildIdentityManageOption {
	return func(c *buildIdentityConfig) {
		if clk != nil {
			c.clock = clk
		}
	}
}

// WithIdentityLogger injects a logger. Defaults to slog.New(slog.DiscardHandler).
// To see verbose output during local debugging, swap in:
//
//	WithIdentityLogger(slog.New(slog.NewTextHandler(os.Stderr, nil)))
func WithIdentityLogger(l *slog.Logger) BuildIdentityManageOption {
	return func(c *buildIdentityConfig) {
		if l != nil {
			c.logger = l
		}
	}
}

// WithIdentityTokenIssuer injects a custom TokenIssuer. When not provided a
// no-op stub is used (sufficient for Create/Lock/Unlock/Delete paths that do
// not call ChangePassword). Tests exercising ChangePassword must inject a real
// sessionlogin.Service.
// See cells/accesscore/slices/sessionlogin.Service which implements TokenIssuer.
func WithIdentityTokenIssuer(ti identitymanage.TokenIssuer) BuildIdentityManageOption {
	return func(c *buildIdentityConfig) {
		if ti != nil {
			c.issuer = ti
		}
	}
}

// BuildIdentityManageService constructs a ready-to-use *identitymanage.Service
// along with the AccessFixture and outboxtest.Recorder it is wired to.
//
// Default wiring:
//   - AccessFixture: fresh NewAccessFixture(t, clock.Real())
//   - invalidator: shares the fixture's UserRepository (same store) plus fresh
//     in-memory session and refresh stores from internal/testutil
//   - TxManager: fixture.TxRunner() (store-paired, full atomic semantics)
//   - Emitter: outboxtest.NewRecorder()
//   - Clock: clock.Real()
//   - Logger: slog.New(slog.DiscardHandler)
//   - TokenIssuer: stubTokenIssuer (returns empty TokenPair; fine for non-ChangePassword paths)
//   - WithLastAdminProtection(fixture.RoleRepository())
//
// The returned fixture is the same one used internally — seed via
// fixture.SeedUser / SeedRole / SeedAssignment, then assert via
// fixture.UserRepository() / RoleRepository().
func BuildIdentityManageService(
	t *testing.T,
	opts ...BuildIdentityManageOption,
) (*identitymanage.Service, *AccessFixture, *outboxtest.Recorder) {
	t.Helper()

	cfg := &buildIdentityConfig{}
	for _, o := range opts {
		o(cfg)
	}

	if cfg.clock == nil {
		cfg.clock = clock.Real()
	}
	if cfg.logger == nil {
		cfg.logger = slog.New(slog.DiscardHandler)
	}
	if cfg.issuer == nil {
		cfg.issuer = stubTokenIssuer{}
	}
	if cfg.fixture == nil {
		cfg.fixture = NewAccessFixture(t, cfg.clock)
	}

	// Construct the invalidator sharing the fixture's UserRepository so that
	// identitymanage and the invalidator read/write the same user store.
	inv := NewCredentialInvalidator(t,
		WithInvalidatorUsers(cfg.fixture.UserRepository()),
	)

	rec := outboxtest.NewRecorder()

	svc, err := identitymanage.NewService(
		cfg.fixture.UserRepository(),
		inv,
		cfg.logger,
		identitymanage.WithEmitter(rec),
		identitymanage.WithTxManager(cfg.fixture.TxRunner()),
		identitymanage.WithClock(cfg.clock),
		identitymanage.WithTokenIssuer(cfg.issuer),
		identitymanage.WithLastAdminProtection(cfg.fixture.RoleRepository()),
	)
	if err != nil {
		t.Fatalf("BuildIdentityManageService: %v", err)
	}

	return svc, cfg.fixture, rec
}

// BuildConfigReceiveOption configures BuildConfigReceiveService.
type BuildConfigReceiveOption func(*buildReceiveConfig)

type buildReceiveConfig struct {
	configGetter *FakeConfigGetter
	logger       *slog.Logger
}

// WithConfigReceiveConfigGetter injects a FakeConfigGetter into the configreceive
// service so that GetEntry calls can be observed and stubbed in tests.
func WithConfigReceiveConfigGetter(g *FakeConfigGetter) BuildConfigReceiveOption {
	return func(c *buildReceiveConfig) {
		if g != nil {
			c.configGetter = g
		}
	}
}

// WithConfigReceiveLogger injects a logger. Defaults to slog.New(slog.DiscardHandler).
// See WithIdentityLogger for stderr-swap example.
func WithConfigReceiveLogger(l *slog.Logger) BuildConfigReceiveOption {
	return func(c *buildReceiveConfig) {
		if l != nil {
			c.logger = l
		}
	}
}

// BuildConfigReceiveService constructs a ready-to-use *configreceive.Service.
//
// Default wiring:
//   - ConfigGetter: NewFakeConfigGetter(nil) — all keys return ErrConfigNotFound
//   - Logger: slog.New(slog.DiscardHandler)
func BuildConfigReceiveService(t *testing.T, opts ...BuildConfigReceiveOption) *configreceive.Service {
	t.Helper()

	cfg := &buildReceiveConfig{}
	for _, o := range opts {
		o(cfg)
	}

	if cfg.logger == nil {
		cfg.logger = slog.New(slog.DiscardHandler)
	}
	if cfg.configGetter == nil {
		cfg.configGetter = NewFakeConfigGetter(nil)
	}

	return configreceive.NewService(
		cfg.logger,
		configreceive.WithConfigGetter(cfg.configGetter),
	)
}
