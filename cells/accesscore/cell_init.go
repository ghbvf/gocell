package accesscore

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/cells/accesscore/internal/adminprovision"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/slices/authorizationdecide"
	"github.com/ghbvf/gocell/cells/accesscore/slices/configreceive"
	"github.com/ghbvf/gocell/cells/accesscore/slices/identitymanage"
	"github.com/ghbvf/gocell/cells/accesscore/slices/rbacassign"
	"github.com/ghbvf/gocell/cells/accesscore/slices/rbaccheck"
	"github.com/ghbvf/gocell/cells/accesscore/slices/sessionlogin"
	"github.com/ghbvf/gocell/cells/accesscore/slices/sessionlogout"
	"github.com/ghbvf/gocell/cells/accesscore/slices/sessionrefresh"
	"github.com/ghbvf/gocell/cells/accesscore/slices/sessionvalidate"
	"github.com/ghbvf/gocell/cells/accesscore/slices/setup"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
)

// resolveEmitter delegates to cell.ResolveCellEmitter (mutual exclusion +
// WithEmitter durable guard + ResolveEmitter delegation + L2 non-durable
// warn) and applies the per-cell rbacEmitterMode side-effect.
//
// accesscore uses DirectPublishFailClosed: security topics (session.*, user.*,
// role.*) must not drop on publisher failure. Per-entry fail-open opt-in is
// outbox.Entry.FailurePolicy — archtest OUTBOX-TOPIC-FAILOPEN-01 bans opt-in
// for security topics.
//
// txRunnerForEmitter is nil when operating in publisher-only demo mode (no
// outboxWriter), so that the ResolveEmitter pairing invariant is not violated.
// c.txRunner is still propagated to slice services in initSlices.
//
// ref: kubernetes/client-go rest.RESTClientFor — factory-composed typed client.
func (c *AccessCore) resolveEmitter(mode cell.DurabilityMode) error {
	txRunnerForEmitter := c.txRunner
	if c.pendingOutboxWriter == nil {
		txRunnerForEmitter = nil
	}
	outcome, err := cell.ResolveCellEmitter(cell.CellEmitterInputs{
		EmitterConfig: cell.EmitterConfig{
			CellID:            "accesscore",
			Mode:              mode,
			Publisher:         c.pendingOutboxPub,
			OutboxWriter:      c.pendingOutboxWriter,
			TxRunner:          txRunnerForEmitter,
			Logger:            c.logger,
			DirectPublishMode: outbox.DirectPublishFailClosed,
			MetricsProvider:   c.metricsProvider,
			Clock:             c.clk,
		},
		PreResolved:      c.emitter,
		ConsistencyLevel: c.ConsistencyLevel(),
	})
	if err != nil {
		return err
	}
	c.emitter = outcome.Emitter
	c.rbacEmitterMode = outcome.Durable
	c.pendingOutboxPub = nil
	c.pendingOutboxWriter = nil
	return nil
}

// initValidate performs fail-fast validation of required dependencies before
// constructing slices. Extracted from Init to reduce cognitive complexity.
func (c *AccessCore) initValidate(durabilityMode cell.DurabilityMode) error {
	if err := c.resolveEmitter(durabilityMode); err != nil {
		return err
	}

	if c.jwtIssuer == nil || c.jwtVerifier == nil {
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthKeyInvalid,
			"RS256 key pair required: use WithJWTIssuer and WithJWTVerifier")
	}
	if c.userRepo == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore requires a user repository: use WithUserRepository or WithInMemoryDefaults")
	}
	if c.sessionRepo == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore requires a session repository: use WithSessionRepository or WithInMemoryDefaults")
	}
	if c.roleRepo == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore requires a role repository: use WithRoleRepository or WithInMemoryDefaults")
	}
	if c.refreshStore == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellMissingTokenIssuer,
			"refresh.Store required: use WithRefreshStore (durable) or WithInMemoryDefaults (demo)")
	}
	if err := c.initRefreshGC(); err != nil {
		return err
	}
	if c.cursorCodec == nil {
		if durabilityMode == cell.DurabilityDurable {
			return errcode.New(errcode.KindInternal, errcode.ErrCellMissingCodec,
				"accesscore durable mode requires a cursor codec; "+
					"use WithCursorCodec(query.NewCursorCodec(secret)) — "+
					"the built-in demo key is public in the source tree")
		}
		codec, err := query.NewCursorCodec([]byte("gocell-demo-ACCESS-CORE-key-32!!"))
		if err != nil {
			return err
		}
		c.cursorCodec = codec
		c.logger.Warn("accesscore: using default cursor codec (demo mode)")
	}
	c.rbacRunMode = query.RunModeForDemo(durabilityMode == cell.DurabilityDemo)
	// resolveEmitter (called above) enforces the (OutboxWriter, TxRunner)
	// pairing invariant using the original c.txRunner; only after it
	// succeeds do we install the demoTxRunner fallback so slice constructors
	// see a non-nil TxRunner.
	if c.txRunner == nil {
		c.logger.Warn("accesscore: using cell.DemoTxRunner (demo mode)",
			slog.String("durability_mode", durabilityMode.String()))
		c.txRunner = cell.DemoTxRunner{}
	}
	// Guard: DemoTxRunner implements Nooper — reject it in DurabilityDurable mode
	// so that assemblies that forget to wire a real TxRunner fail at Init() time.
	if err := cell.CheckNotNoop(durabilityMode, "accesscore", c.txRunner); err != nil {
		return err
	}
	return nil
}

func (c *AccessCore) initRefreshGC() error {
	if !c.refreshGCEnabled {
		return nil
	}
	if c.refreshGCInterval <= 0 {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "accesscore refresh GC interval must be positive")
	}
	if c.refreshGCRetention <= 0 {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig, "accesscore refresh GC retention must be positive")
	}
	provider := c.metricsProvider
	if provider == nil {
		provider = metrics.NopProvider{}
	}
	collector, err := refresh.NewProviderGCCollector(provider)
	if err != nil {
		return err
	}
	c.refreshGCCollector = collector
	return nil
}

// initSlices constructs all 9 slice services and handlers in declaration
// order. The function is a thin sequential composition root — breaking it
// further would scatter the dependency-injection wiring across multiple
// helpers and obscure the cross-slice ordering constraints (e.g. login must
// be constructed before identity-manage to inject TokenIssuer).
//
//nolint:funlen // sequential cell composition root; readability over funlen budget
func (c *AccessCore) initSlices() error {
	// session-login must be constructed before identity-manage because
	// ChangePassword injects loginSvc as the TokenIssuer.
	loginOpts := []sessionlogin.Option{
		sessionlogin.WithEmitter(c.emitter),
		sessionlogin.WithTxManager(c.txRunner),
		sessionlogin.WithClock(c.clk),
	}
	loginSvc, err := sessionlogin.NewService(c.userRepo, c.sessionRepo, c.roleRepo, c.refreshStore, c.jwtIssuer, c.logger, loginOpts...)
	if err != nil {
		return err
	}
	c.loginHandler = sessionlogin.NewHandler(loginSvc)
	c.AddSlice(cell.NewBaseSlice("sessionlogin", "accesscore", cell.L2))

	// identity-manage: inject loginSvc as TokenIssuer for ChangePassword.
	identityOpts := []identitymanage.Option{
		identitymanage.WithEmitter(c.emitter),
		identitymanage.WithTxManager(c.txRunner),
		identitymanage.WithClock(c.clk),
	}
	identityOpts = append(identityOpts, identitymanage.WithTokenIssuer(loginSvc))
	identitySvc, err := identitymanage.NewService(c.userRepo, c.sessionRepo, c.refreshStore, c.logger, identityOpts...)
	if err != nil {
		return err
	}
	c.identityHandler = identitymanage.NewHandler(identitySvc)
	c.AddSlice(cell.NewBaseSlice("identitymanage", "accesscore", cell.L1))

	// session-validate (before session-refresh: provides session-aware verifier)
	c.validateSvc = sessionvalidate.NewService(c.jwtVerifier, c.sessionRepo, c.logger, c.clk)
	c.AddSlice(cell.NewBaseSlice("sessionvalidate", "accesscore", cell.L0))

	// session-refresh uses refresh.Store for token state validation and
	// rotation. No JWT verifier is needed — the opaque wire format is
	// validated by the store itself; any malformed input (including an
	// access JWT replay attempt) returns ErrRejected.
	refreshSvc, err := sessionrefresh.NewService(
		c.sessionRepo, c.roleRepo, c.userRepo, c.refreshStore,
		c.jwtIssuer, c.logger,
		sessionrefresh.WithClock(c.clk),
		sessionrefresh.WithTxManager(c.txRunner),
	)
	if err != nil {
		return err
	}
	c.refreshHandler = sessionrefresh.NewHandler(refreshSvc)
	c.AddSlice(cell.NewBaseSlice("sessionrefresh", "accesscore", cell.L1))

	// session-logout — cascades revocation to refresh.Store so logout
	// invalidates the full refresh chain, not just the access session.
	logoutOpts := []sessionlogout.Option{sessionlogout.WithEmitter(c.emitter), sessionlogout.WithTxManager(c.txRunner)}
	logoutSvc, err := sessionlogout.NewService(c.sessionRepo, c.refreshStore, c.logger, logoutOpts...)
	if err != nil {
		return err
	}
	c.logoutHandler = sessionlogout.NewHandler(logoutSvc)
	c.AddSlice(cell.NewBaseSlice("sessionlogout", "accesscore", cell.L2))

	// authorization-decide
	c.authzSvc = authorizationdecide.NewService(c.roleRepo, c.logger)
	c.AddSlice(cell.NewBaseSlice("authorizationdecide", "accesscore", cell.L0))

	// rbac-check
	rbacSvc, err := rbaccheck.NewService(c.roleRepo, c.cursorCodec, c.logger, c.rbacRunMode)
	if err != nil {
		return err
	}
	c.rbacHandler = rbaccheck.NewHandler(rbacSvc)
	c.AddSlice(cell.NewBaseSlice("rbaccheck", "accesscore", cell.L0))

	// rbac-assign — durable mode (outboxWriter + txRunner) upgrades to L2 OutboxFact;
	// demo mode (both nil) stays at L0 (in-memory repos, synchronous dual-write).
	if err := c.initRbacAssign(); err != nil {
		return err
	}

	// rbac-session-sync consumer: handles role-change events and invalidates sessions.
	c.rbacSessionConsumer = sessionlogout.NewConsumer(c.sessionRepo, c.logger)

	// config-receive: subscribes to config state-sync events from configcore.
	// WithConfigGetter is optional — nil disables the cross-cell GetEntry fetch.
	c.configReceiveSvc = configreceive.NewService(c.logger,
		configreceive.WithConfigGetter(c.configGetter),
		configreceive.WithConfigEventCollector(c.configEventCollector),
	)
	c.AddSlice(cell.NewBaseSlice("configreceive", "accesscore", cell.L3))

	// setup: first-run admin provisioning.
	// Uses shared adminprovision.Provisioner so semantics match initialadmin.
	if c.bootstrapAuth == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore: WithBootstrapAuth is required (auth.bootstrap:true contracts "+
				"need a per-route replacement authenticator; composition root must wire "+
				"runtime/auth.NewBootstrapMiddleware via WithBootstrapAuth). "+
				"See docs/architecture/202605061600-adr-bootstrap-admin-boundary.md §D1.")
	}
	setupProv, err := adminprovision.NewProvisioner(c.userRepo, c.roleRepo, c.logger, uuid.NewString, c.clk)
	if err != nil {
		return err
	}
	setupSvc, err := setup.NewService(setupProv, c.logger,
		setup.WithEmitter(c.emitter),
		setup.WithTxManager(c.txRunner),
		setup.WithSetupLock(c.setupLock),
	)
	if err != nil {
		return err
	}
	c.setupHandler = setup.NewHandler(setupSvc, c.bootstrapAuth)
	c.AddSlice(cell.NewBaseSlice("setup", "accesscore", cell.L2))
	return nil
}

// initRbacAssign constructs the rbac-assign slice. Extracted to keep initSlices
// within cognitive complexity bounds.
func (c *AccessCore) initRbacAssign() error {
	rbacOpts := []rbacassign.Option{rbacassign.WithTxManager(c.txRunner)}
	if c.rbacEmitterMode {
		rbacOpts = append(rbacOpts, rbacassign.WithEmitter(c.emitter))
	}
	rbacAssignSvc, err := rbacassign.NewService(c.roleRepo, c.sessionRepo, c.logger, rbacOpts...)
	if err != nil {
		return err
	}
	c.rbacAssignHandler = rbacassign.NewHandler(rbacAssignSvc)
	rbacAssignLevel := cell.L0
	if c.rbacEmitterMode {
		rbacAssignLevel = cell.L2
	}
	c.AddSlice(cell.NewBaseSlice("rbacassign", "accesscore", rbacAssignLevel))
	return nil
}

// initInternal is the K#04 codegen escape hatch: business init that cannot
// be generated (emitter resolve, slice service construction, health probes,
// lifecycle hooks). cell_gen.go::Init calls it after BaseCell.Init and before
// mounting the generated route-group and subscribe blocks. This is a permanent
// convention, not a transitional shim.
//
//nolint:unparam // ctx is part of the K#04 initInternal contract; unused here, used by other cells (devicecell)
func (c *AccessCore) initInternal(ctx context.Context, reg cell.Registry) error {
	clock.MustHaveClock(c.clk, "accesscore.initInternal")

	// WithInMemoryDefaults defers sessionRepo and refreshStore construction
	// to here so that c.clk is available.
	//
	// HAZARD: session repository stays mem in S3+S5 even when PG storage backend
	// is selected. accesscore's PG-mode TxRunner writes user/role/outbox to PG
	// but session/refresh to mem — sessionlogin.persistSessionWithRefresh runs
	// mem writes inside a real PG tx. PG rollback does NOT unwind mem
	// session/refresh state. S4 wires the runtime session.Store + PG refresh
	// store and removes this hazard. Backlog: S4-PG-SESSION-REFRESH-WIRING-COMPLETE-01.
	if c.useInMemoryDefaults && c.sessionRepo == nil {
		c.sessionRepo = mem.NewSessionRepository(c.clk)
	}
	if c.useInMemoryDefaults && c.refreshStore == nil {
		rstore, rstoreErr := refreshmem.New(defaultRefreshPolicy, c.clk, nil)
		if rstoreErr != nil {
			return errcode.Wrap(errcode.KindInternal, errcode.ErrCellInvalidConfig, "accesscore: init in-memory refresh store", rstoreErr)
		}
		c.refreshStore = rstore
	}

	durabilityMode := reg.DurabilityMode()

	if err := c.initValidate(durabilityMode); err != nil {
		return err
	}
	if err := c.initSlices(); err != nil {
		return err
	}

	// Route groups and subscriptions removed: cell_gen.go owns Init and renders them.
	c.registerHealthAndLifecycle(reg)

	return nil
}

// registerHealthAndLifecycle registers health probes and lifecycle hooks into reg.
func (c *AccessCore) registerHealthAndLifecycle(reg cell.Registry) {
	if hc, ok := c.sessionRepo.(interface {
		Health(context.Context) error
	}); ok {
		reg.Health("session_store_ready", hc.Health)
	}
	if hc, ok := c.emitter.(cell.HealthProber); ok {
		for k, v := range hc.Probes() {
			reg.Health(k, v)
		}
	}
	if c.refreshGCEnabled {
		reg.Lifecycle(c.refreshGCHook())
	}
}
