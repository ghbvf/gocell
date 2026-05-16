package accesscore

import (
	"context"
	"log/slog"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/cells/accesscore/internal/adminprovision"
	"github.com/ghbvf/gocell/cells/accesscore/internal/credentialinvalidate"
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
	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
)

// resolveEmitter delegates to cell.ResolveCellEmitter (mutual exclusion +
// WithEmitter durable guard + ResolveEmitter delegation + L2 non-durable
// warn).
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
	if err := c.validateRequiredDeps(); err != nil {
		return err
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
		c.logger.Warn("accesscore: using cell.DemoCellTxManager (demo mode)",
			slog.String("durability_mode", durabilityMode.String()))
		c.txRunner = cell.DemoCellTxManager()
	}
	// Guard: DemoTxRunner implements Nooper — reject it in DurabilityDurable mode
	// so that assemblies that forget to wire a real TxRunner fail at Init() time.
	if err := cell.CheckNotNoop(durabilityMode, "accesscore", c.txRunner); err != nil {
		return err
	}
	return nil
}

// validateRequiredDeps checks the closed set of mandatory dependencies that
// every accesscore deployment must wire. Extracted from initValidate to keep
// the parent's cognitive complexity ≤ 15.
func (c *AccessCore) validateRequiredDeps() error {
	if c.jwtIssuer == nil || c.jwtVerifier == nil {
		return errcode.New(errcode.KindUnauthenticated, errcode.ErrAuthKeyInvalid,
			"RS256 key pair required: use WithJWTIssuer and WithJWTVerifier")
	}
	if c.userRepo == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore requires a user repository: use WithUserRepository")
	}
	if c.sessionStoreNil || c.sessionStore == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore requires a session store: use WithSessionStore")
	}
	if c.roleRepo == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore requires a role repository: use WithRoleRepository")
	}
	if c.refreshStore == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellMissingTokenIssuer,
			"refresh.Store required: use WithRefreshStore")
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
		sessionlogin.WithSessionTTL(DefaultRefreshMaxAge),
	}
	loginSvc, err := sessionlogin.NewService(c.userRepo, c.sessionStore, c.roleRepo, c.refreshStore, c.jwtIssuer, c.logger, loginOpts...)
	if err != nil {
		return err
	}
	c.loginHandler = sessionlogin.NewHandler(loginSvc)
	c.AddSlice(cell.NewBaseSlice("sessionlogin", "accesscore", cellvocab.L2))

	// credentialinvalidate: shared invalidator for identity-manage, rbac-assign,
	// and session-refresh. Atomically bumps authz_epoch, revokes all sessions, and
	// revokes all refresh tokens for a subject when a credential-invalidating event
	// (password change, role assignment, token reuse) is detected.
	inv, err := credentialinvalidate.New(c.userRepo, c.sessionStore, c.refreshStore)
	if err != nil {
		return err
	}
	c.invalidator = inv

	// identity-manage: inject loginSvc as TokenIssuer for ChangePassword.
	identityOpts := []identitymanage.Option{
		identitymanage.WithEmitter(c.emitter),
		identitymanage.WithTxManager(c.txRunner),
		identitymanage.WithClock(c.clk),
		identitymanage.WithLastAdminProtection(c.roleRepo),
	}
	identityOpts = append(identityOpts, identitymanage.WithTokenIssuer(loginSvc))
	identitySvc, err := identitymanage.NewService(c.userRepo, c.invalidator, c.logger, identityOpts...)
	if err != nil {
		return err
	}
	c.identityHandler = identitymanage.NewHandler(identitySvc)
	c.AddSlice(cell.NewBaseSlice("identitymanage", "accesscore", cellvocab.L1))

	// session-validate (before session-refresh: provides session-aware verifier)
	validateSvc, err := sessionvalidate.NewService(c.jwtVerifier, c.sessionStore, c.userRepo, c.logger)
	if err != nil {
		return err
	}
	c.validateSvc = validateSvc
	c.AddSlice(cell.NewBaseSlice("sessionvalidate", "accesscore", cellvocab.L0))

	// session-refresh uses refresh.Store for token state validation and
	// rotation. No JWT verifier is needed — the opaque wire format is
	// validated by the store itself; any malformed input (including an
	// access JWT replay attempt) returns ErrRejected.
	refreshSvc, err := sessionrefresh.NewService(
		c.sessionStore, c.roleRepo, c.userRepo, c.refreshStore,
		c.jwtIssuer, c.logger,
		sessionrefresh.WithClock(c.clk),
		sessionrefresh.WithTxManager(c.txRunner),
		sessionrefresh.WithInvalidator(c.invalidator),
	)
	if err != nil {
		return err
	}
	c.refreshHandler = sessionrefresh.NewHandler(refreshSvc)
	c.AddSlice(cell.NewBaseSlice("sessionrefresh", "accesscore", cellvocab.L1))

	// session-logout — cascades revocation to refresh.Store so logout
	// invalidates the full refresh chain, not just the access session.
	logoutOpts := []sessionlogout.Option{sessionlogout.WithEmitter(c.emitter), sessionlogout.WithTxManager(c.txRunner)}
	logoutSvc, err := sessionlogout.NewService(c.sessionStore, c.refreshStore, c.logger, logoutOpts...)
	if err != nil {
		return err
	}
	c.logoutHandler = sessionlogout.NewHandler(logoutSvc)
	c.AddSlice(cell.NewBaseSlice("sessionlogout", "accesscore", cellvocab.L2))

	// authorization-decide
	authzSvc, err := authorizationdecide.NewService(c.roleRepo, c.logger)
	if err != nil {
		return err
	}
	c.authzSvc = authzSvc
	c.AddSlice(cell.NewBaseSlice("authorizationdecide", "accesscore", cellvocab.L0))

	// rbac-check
	rbacSvc, err := rbaccheck.NewService(c.roleRepo, c.cursorCodec, c.logger, c.rbacRunMode)
	if err != nil {
		return err
	}
	c.rbacHandler = rbaccheck.NewHandler(rbacSvc)
	c.AddSlice(cell.NewBaseSlice("rbaccheck", "accesscore", cellvocab.L0))

	// rbac-assign is always L2 OutboxFact (locked by RBACASSIGN-L2-STATIC-01 archtest);
	// runtime emit fidelity depends on resolveEmitter output — see initRbacAssign godoc.
	if err := c.initRbacAssign(); err != nil {
		return err
	}

	// rbac-session-sync consumer: handles role-change events and invalidates sessions.
	c.rbacSessionConsumer = sessionlogout.NewConsumer(c.logger)

	// config-receive: subscribes to config state-sync events from configcore.
	// WithConfigGetter is optional — nil disables the cross-cell GetEntry fetch.
	c.configReceiveSvc = configreceive.NewService(c.logger,
		configreceive.WithConfigGetter(c.configGetter),
		configreceive.WithConfigEventCollector(c.configEventCollector),
	)
	c.AddSlice(cell.NewBaseSlice("configreceive", "accesscore", cellvocab.L3))

	// setup: first-run admin provisioning.
	// Uses shared adminprovision.Provisioner so semantics match initialadmin.
	if c.casProtocol == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore: WithCASProtocol is required for ChangePassword concurrent-write guard (S6); "+
				"composition root must wire cas.MustNewProtocol(cas.WithVersionField(\"password_version\")) "+
				"via WithCASProtocol")
	}
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
	c.AddSlice(cell.NewBaseSlice("setup", "accesscore", cellvocab.L2))
	return nil
}

// initRbacAssign constructs the rbac-assign slice. rbacassign is L2 OutboxFact:
// the slice's behavioral contract is to emit role.assigned / role.revoked
// outbox facts atomically inside RunInTx. The consistency level is declared
// `cellvocab.L2` independent of runtime mode (RBACASSIGN-L2-STATIC-01 archtest
// locks the literal).
//
// Runtime emit fidelity depends on cell.ResolveCellEmitter's output:
//   - durable mode (publisher + writer + txRunner) → WriterEmitter writes a row
//     in the outbox table; the row + role write co-commit, providing real L2
//     atomicity end-to-end.
//   - publisher-only demo (no writer) → DirectEmitter synchronously publishes
//     without a durable outbox row. The slice still drives the funnel
//     (RunInTx → emit) but there is no row to replay on failure — this is
//     test/demo fidelity only, not L2 atomicity.
func (c *AccessCore) initRbacAssign() error {
	rbacAssignSvc, err := rbacassign.NewService(c.roleRepo, c.invalidator, c.logger,
		rbacassign.WithEmitter(c.emitter),
		rbacassign.WithTxManager(c.txRunner),
	)
	if err != nil {
		return err
	}
	c.rbacAssignHandler = rbacassign.NewHandler(rbacAssignSvc)
	c.AddSlice(cell.NewBaseSlice("rbacassign", "accesscore", cellvocab.L2))
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
	// session.Store satisfies cell.RepoHealthProber via its RepoReady method.
	// Use the typed funnel instead of an anonymous duck-type assertion so
	// CELL-REPO-READYZ-PROBE-01 archtest can enforce the canonical form.
	cell.RegisterRepoReadiness(reg, "session_store_ready", c.sessionStore)
	if hc, ok := c.emitter.(cell.HealthProber); ok {
		for k, v := range hc.Probes() {
			reg.Health(k, v)
		}
	}
	if c.refreshGCEnabled {
		reg.Lifecycle(c.refreshGCHook())
	}
}
