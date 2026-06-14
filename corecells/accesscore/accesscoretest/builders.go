package accesscoretest

import (
	"context"
	"log/slog"
	"testing"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/corecells/accesscore/slices/configreceive"
	"github.com/ghbvf/gocell/corecells/accesscore/slices/identitymanage"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/outbox/outboxtest"
	obmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
)

// failingTokenIssuer is the default identitymanage.TokenIssuer wired by
// BuildIdentityManageService. It t.Fatal's the moment IssueForUser is called,
// which translates "test exercises ChangePassword but forgot to inject a real
// issuer" from a silent false-green (empty TokenPair, nil error) into an
// immediate hard failure with the exact remediation in the message.
//
// Non-ChangePassword paths (Create / Lock / Unlock / Delete / Suspend) never
// call IssueForUser, so the default is harmless for them.
//
// ref: testing/iotest.ErrReader — same fail-loud shape for "this method
// should never be called in this test".
type failingTokenIssuer struct{ t *testing.T }

func (f failingTokenIssuer) IssueForUser(context.Context, string) (dto.TokenPair, error) {
	f.t.Helper()
	f.t.Fatal("BuildIdentityManageService: default TokenIssuer was called; " +
		"ChangePassword paths must inject a real issuer via WithIdentityTokenIssuer " +
		"(typically the sessionlogin.Service)")
	return dto.TokenPair{}, nil
}

// Compile-time: failingTokenIssuer must satisfy the narrow identitymanage.TokenIssuer
// interface.
var _ identitymanage.TokenIssuer = failingTokenIssuer{}

// BuildIdentityManageOption configures BuildIdentityManageService.
type BuildIdentityManageOption func(*buildIdentityConfig)

type buildIdentityConfig struct {
	fixture     *AccessFixture
	clock       clock.Clock
	logger      *slog.Logger
	issuer      identitymanage.TokenIssuer
	invalidator *CredentialInvalidator
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

// WithIdentityTokenIssuer injects a custom TokenIssuer. When not provided the
// default failingTokenIssuer is wired, which fails the test on IssueForUser
// call. Tests exercising ChangePassword must inject a real
// sessionlogin.Service.
// See corecells/accesscore/slices/sessionlogin.Service which implements TokenIssuer.
func WithIdentityTokenIssuer(ti identitymanage.TokenIssuer) BuildIdentityManageOption {
	return func(c *buildIdentityConfig) {
		if ti != nil {
			c.issuer = ti
		}
	}
}

// WithIdentityInvalidator overrides the credential invalidator. By default
// BuildIdentityManageService constructs an invalidator wired to the fixture
// (so user/session/refresh state stays paired). Use this escape hatch only
// when a test needs to substitute a custom invalidator built against the
// same fixture — passing nil keeps the default.
//
// The parameter is the public opaque *CredentialInvalidator returned by
// NewCredentialInvalidator; the unwrap to internal/credentialinvalidate
// happens once inside BuildIdentityManageService, keeping the internal
// type unreachable from external test packages.
func WithIdentityInvalidator(inv *CredentialInvalidator) BuildIdentityManageOption {
	return func(c *buildIdentityConfig) {
		if inv != nil {
			c.invalidator = inv
		}
	}
}

// BuildIdentityManageService constructs a ready-to-use *identitymanage.Service
// along with the AccessFixture and outboxtest.Recorder it is wired to.
//
// Default wiring:
//   - AccessFixture: fresh NewAccessFixture(t, clock.Real())
//   - invalidator: NewCredentialInvalidator(t, WithInvalidatorFixture(fixture))
//     — shares the fixture's UserRepository + session.MemStore + refresh.Store
//     so any sessionlogin.Service injected via WithIdentityTokenIssuer sees
//     the same session/refresh state the invalidator revokes against
//   - TxManager: fixture.TxRunner() (store-paired, full atomic semantics)
//   - Emitter: outboxtest.NewRecorder()
//   - Clock: clock.Real()
//   - Logger: slog.New(slog.DiscardHandler)
//   - TokenIssuer: failingTokenIssuer (t.Fatal on IssueForUser; replace via
//     WithIdentityTokenIssuer for ChangePassword paths)
//   - roleRepo: fixture.RoleRepository (required positional param) — wires the
//     at-least-one-effective-admin guard
//
// The returned fixture is the same one used internally — seed via
// fixture.SeedUser / SeedRole / SeedAssignment, then assert via
// fixture.GetUser / GetRole / UserRoles.
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
		cfg.issuer = failingTokenIssuer{t: t}
	}
	if cfg.fixture == nil {
		cfg.fixture = NewAccessFixture(t, cfg.clock)
	}
	if cfg.invalidator == nil {
		cfg.invalidator = NewCredentialInvalidator(t, WithInvalidatorFixture(cfg.fixture))
	}

	rec := outboxtest.NewRecorder()

	// Unwrap the opaque CredentialInvalidator handle exactly once, here
	// inside the testutil package — the single sanctioned touchpoint of
	// the internal/credentialinvalidate type per the wrapper's Hard funnel
	// (see credential_invalidator.go godoc).
	rawInv := cfg.invalidator.inv

	svc, err := identitymanage.NewService(
		cfg.clock,
		cfg.fixture.bundle.UserRepository(),
		rawInv,
		cfg.logger,
		cfg.fixture.bundle.RoleRepository(),
		identitymanage.WithEmitter(rec.CellEmitter()),
		identitymanage.WithTxManager(cfg.fixture.TxRunner()),
		identitymanage.WithTokenIssuer(cfg.issuer),
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
	collector    obmetrics.ConfigEventCollector
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

// WithConfigReceiveCollector injects a config event collector so tests can
// observe ack / stale / permanent_error metric attribution downstream of
// HandleEntryUpserted / HandleEntryDeleted.
func WithConfigReceiveCollector(c obmetrics.ConfigEventCollector) BuildConfigReceiveOption {
	return func(cfg *buildReceiveConfig) {
		if c != nil {
			cfg.collector = c
		}
	}
}

// BuildConfigReceiveService constructs a ready-to-use *configreceive.Service.
//
// Default wiring:
//   - ConfigGetter: NewFakeConfigGetter(nil) — all keys return ErrConfigRepoNotFound
//   - Logger: slog.New(slog.DiscardHandler)
//   - ConfigEventCollector: not set — configreceive.NewService falls back to its
//     own noop collector. Inject via WithConfigReceiveCollector to assert
//     metric attribution.
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

	serviceOpts := []configreceive.Option{configreceive.WithConfigGetter(cfg.configGetter)}
	if cfg.collector != nil {
		serviceOpts = append(serviceOpts, configreceive.WithConfigEventCollector(cfg.collector))
	}
	return configreceive.NewService(cfg.logger, serviceOpts...)
}
