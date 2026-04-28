package accesscore

import (
	"context"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/cells/accesscore/initialadmin"
	"github.com/ghbvf/gocell/cells/accesscore/internal/adminprovision"
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
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
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
// ref: kubernetes/client-go rest.RESTClientFor — factory-composed typed client.
func (c *AccessCore) resolveEmitter(mode cell.DurabilityMode) error {
	outcome, err := cell.ResolveCellEmitter(cell.CellEmitterInputs{
		EmitterConfig: cell.EmitterConfig{
			CellID:            "accesscore",
			Mode:              mode,
			Publisher:         c.pendingOutboxPub,
			OutboxWriter:      c.pendingOutboxWriter,
			TxRunner:          c.txRunner,
			Logger:            c.logger,
			DirectPublishMode: outbox.DirectPublishFailClosed,
			MetricsProvider:   c.metricsProvider,
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
	c.txRunner = persistence.RunnerOrNoop(c.txRunner)
	return nil
}

// initValidate performs fail-fast validation of required dependencies before
// constructing slices. Extracted from Init to reduce cognitive complexity.
func (c *AccessCore) initValidate(deps cell.Dependencies) error {
	if err := c.resolveEmitter(deps.DurabilityMode); err != nil {
		return err
	}

	if c.jwtIssuer == nil || c.jwtVerifier == nil {
		return errcode.New(errcode.ErrAuthKeyInvalid,
			"RS256 key pair required: use WithJWTIssuer and WithJWTVerifier")
	}
	if c.userRepo == nil {
		return errcode.New(errcode.ErrCellInvalidConfig,
			"accesscore requires a user repository: use WithUserRepository or WithInMemoryDefaults")
	}
	if c.sessionRepo == nil {
		return errcode.New(errcode.ErrCellInvalidConfig,
			"accesscore requires a session repository: use WithSessionRepository or WithInMemoryDefaults")
	}
	if c.roleRepo == nil {
		return errcode.New(errcode.ErrCellInvalidConfig,
			"accesscore requires a role repository: use WithRoleRepository or WithInMemoryDefaults")
	}
	if c.refreshStore == nil {
		return errcode.New(errcode.ErrCellMissingTokenIssuer,
			"refresh.Store required: use WithRefreshStore (durable) or WithInMemoryDefaults (demo)")
	}
	if err := c.initRefreshGC(); err != nil {
		return err
	}
	if c.cursorCodec == nil {
		if deps.DurabilityMode == cell.DurabilityDurable {
			return errcode.New(errcode.ErrCellMissingCodec,
				"accesscore durable mode requires a cursor codec; use WithCursorCodec(query.NewCursorCodec(secret)) — the built-in demo key is public in the source tree")
		}
		codec, err := query.NewCursorCodec([]byte("gocell-demo-ACCESS-CORE-key-32!!"))
		if err != nil {
			return err
		}
		c.cursorCodec = codec
		c.logger.Warn("accesscore: using default cursor codec (demo mode)")
	}
	c.rbacRunMode = query.RunModeForDemo(deps.DurabilityMode == cell.DurabilityDemo)
	return nil
}

func (c *AccessCore) initRefreshGC() error {
	if !c.refreshGCEnabled {
		return nil
	}
	if c.refreshGCInterval <= 0 {
		return errcode.New(errcode.ErrCellInvalidConfig, "accesscore refresh GC interval must be positive")
	}
	if c.refreshGCRetention <= 0 {
		return errcode.New(errcode.ErrCellInvalidConfig, "accesscore refresh GC retention must be positive")
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

// initSlices constructs all 9 slice services and handlers.
// Extracted from Init to reduce cognitive complexity.
func (c *AccessCore) initSlices() error {
	// session-login must be constructed before identity-manage because
	// ChangePassword injects loginSvc as the TokenIssuer.
	loginOpts := []sessionlogin.Option{sessionlogin.WithEmitter(c.emitter), sessionlogin.WithTxManager(c.txRunner)}
	loginSvc, err := sessionlogin.NewService(c.userRepo, c.sessionRepo, c.roleRepo, c.refreshStore, c.jwtIssuer, c.logger, loginOpts...)
	if err != nil {
		return err
	}
	c.loginHandler = sessionlogin.NewHandler(loginSvc)
	c.AddSlice(cell.NewBaseSlice("sessionlogin", "accesscore", cell.L2))

	// identity-manage: inject loginSvc as TokenIssuer for ChangePassword.
	identityOpts := []identitymanage.Option{identitymanage.WithEmitter(c.emitter), identitymanage.WithTxManager(c.txRunner)}
	identityOpts = append(identityOpts, identitymanage.WithTokenIssuer(loginSvc))
	identitySvc, err := identitymanage.NewService(c.userRepo, c.sessionRepo, c.refreshStore, c.logger, identityOpts...)
	if err != nil {
		return err
	}
	c.identityHandler = identitymanage.NewHandler(identitySvc)
	c.AddSlice(cell.NewBaseSlice("identitymanage", "accesscore", cell.L1))

	// session-validate (before session-refresh: provides session-aware verifier)
	c.validateSvc = sessionvalidate.NewService(c.jwtVerifier, c.sessionRepo, c.logger)
	c.AddSlice(cell.NewBaseSlice("sessionvalidate", "accesscore", cell.L0))

	// session-refresh uses refresh.Store for token state validation and
	// rotation. No JWT verifier is needed — the opaque wire format is
	// validated by the store itself; any malformed input (including an
	// access JWT replay attempt) returns ErrRejected.
	refreshSvc, err := sessionrefresh.NewService(c.sessionRepo, c.roleRepo, c.userRepo, c.refreshStore, c.jwtIssuer, c.logger)
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
	c.initRbacAssign()

	// rbac-session-sync consumer: handles role-change events and invalidates sessions.
	c.rbacSessionConsumer = sessionlogout.NewConsumer(c.sessionRepo, c.logger)

	// config-receive: subscribes to config state-sync events from configcore.
	// WithConfigGetter is optional — nil disables the cross-cell GetEntry fetch.
	c.configReceiveSvc = configreceive.NewService(c.logger,
		configreceive.WithConfigGetter(c.configGetter),
		configreceive.WithConfigEventCollector(c.configEventCollector),
	)
	c.AddSlice(cell.NewBaseSlice("configreceive", "accesscore", cell.L3))

	// setup: interactive first-run admin provisioning (Public HTTP endpoints).
	// Uses shared adminprovision.Provisioner so semantics match initialadmin.
	setupProv, err := adminprovision.NewProvisioner(c.userRepo, c.roleRepo, c.logger, uuid.NewString)
	if err != nil {
		return err
	}
	setupSvc, err := setup.NewService(setupProv, c.logger,
		setup.WithEmitter(c.emitter),
		setup.WithTxManager(c.txRunner),
	)
	if err != nil {
		return err
	}
	c.setupHandler = setup.NewHandler(setupSvc)
	c.AddSlice(cell.NewBaseSlice("setup", "accesscore", cell.L2))
	return nil
}

// initRbacAssign constructs the rbac-assign slice. Extracted to keep initSlices
// within cognitive complexity bounds.
func (c *AccessCore) initRbacAssign() {
	rbacOpts := []rbacassign.Option{rbacassign.WithTxManager(c.txRunner)}
	if c.rbacEmitterMode {
		rbacOpts = append(rbacOpts, rbacassign.WithEmitter(c.emitter))
	}
	rbacAssignSvc := rbacassign.NewService(c.roleRepo, c.sessionRepo, c.logger, rbacOpts...)
	c.rbacAssignHandler = rbacassign.NewHandler(rbacAssignSvc)
	rbacAssignLevel := cell.L0
	if c.rbacEmitterMode {
		rbacAssignLevel = cell.L2
	}
	c.AddSlice(cell.NewBaseSlice("rbacassign", "accesscore", rbacAssignLevel))
}

// Init constructs all 9 slices and wires the initial-admin Lifecycle (when
// WithInitialAdminBootstrap is active) so LifecycleHooks() remains a pure
// getter — the kernel.LifecycleContributor contract promises "called after
// Cell.Init completes", which is precisely where we inject deps.
func (c *AccessCore) Init(ctx context.Context, deps cell.Dependencies) error {
	if err := c.BaseCell.Init(ctx, deps); err != nil {
		return err
	}
	if err := c.initValidate(deps); err != nil {
		return err
	}
	if err := c.initSlices(); err != nil {
		return err
	}
	if c.initialAdmin != nil {
		// Platform check fails fast at phase2 Init() rather than at phase3b
		// LifecycleHook OnStart, so an unsupported GOOS surfaces before any
		// bootstrap goroutine runs.
		if err := initialadmin.PlatformSupported(); err != nil {
			return err
		}
		c.initialAdmin.Bind(initialadmin.BootstrapDeps{
			UserRepo: c.userRepo,
			RoleRepo: c.roleRepo,
			Logger:   c.logger,
			Clock:    nil,
		}, c.logger)
	}
	return nil
}

// LifecycleHooks implements cell.LifecycleContributor. Returns the initial-admin
// hook when WithInitialAdminBootstrap was applied; nil otherwise (opt-out).
//
// Pure getter by design: the Bind side-effect lives in Init (see above), so
// callers can invoke LifecycleHooks multiple times without double-binding.
// Mirrors fx Hook.callerFrame / Kubernetes controller-runtime Runnable.GetName()
// — getters must not mutate.
func (c *AccessCore) LifecycleHooks() []cell.LifecycleHook {
	var hooks []cell.LifecycleHook
	if c.initialAdmin != nil {
		hooks = append(hooks, c.initialAdmin.Hook())
	}
	if c.refreshGCEnabled {
		hooks = append(hooks, c.refreshGCHook())
	}
	return hooks
}
