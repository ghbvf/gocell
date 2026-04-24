package accesscore

import (
	"context"
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/initialadmin"
	"github.com/ghbvf/gocell/cells/accesscore/slices/authorizationdecide"
	"github.com/ghbvf/gocell/cells/accesscore/slices/configreceive"
	"github.com/ghbvf/gocell/cells/accesscore/slices/identitymanage"
	"github.com/ghbvf/gocell/cells/accesscore/slices/rbacassign"
	"github.com/ghbvf/gocell/cells/accesscore/slices/rbaccheck"
	"github.com/ghbvf/gocell/cells/accesscore/slices/sessionlogin"
	"github.com/ghbvf/gocell/cells/accesscore/slices/sessionlogout"
	"github.com/ghbvf/gocell/cells/accesscore/slices/sessionrefresh"
	"github.com/ghbvf/gocell/cells/accesscore/slices/sessionvalidate"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// initValidate performs fail-fast validation of required dependencies before
// constructing slices. Extracted from Init to reduce cognitive complexity.
func (c *AccessCore) initValidate(deps cell.Dependencies) error {
	// Resolve outbox emitter via shared kernel helper (mirrors configcore/auditcore).
	// Pass raw c.txRunner so ResolveEmitter can detect nil/noop pairing; wrap
	// with RunnerOrNoop only after outcome is determined.
	outcome, err := cell.ResolveEmitter(cell.EmitterConfig{
		CellID:            "accesscore",
		Mode:              deps.DurabilityMode,
		Publisher:         c.publisher,
		OutboxWriter:      c.outboxWriter,
		TxRunner:          c.txRunner,
		Logger:            c.logger,
		DirectPublishMode: outbox.DirectPublishFailOpen,
	})
	if err != nil {
		return err
	}
	c.txRunner = persistence.RunnerOrNoop(c.txRunner)
	c.emitter = outcome.Emitter
	c.rbacEmitterMode = outcome.Durable

	// L2 warning: running without transactional outbox degrades atomicity guarantees.
	if !outcome.Durable && c.ConsistencyLevel() >= cell.L2 {
		c.logger.Warn("accesscore: running without outboxWriter+txRunner, L2 transactional atomicity not guaranteed (demo mode)",
			slog.String("cell", c.ID()),
			slog.Int("consistency_level", int(c.ConsistencyLevel())))
	}

	if c.jwtIssuer == nil || c.jwtVerifier == nil {
		return errcode.New(errcode.ErrAuthKeyInvalid,
			"RS256 key pair required: use WithJWTIssuer and WithJWTVerifier")
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

// initSlices constructs all 9 slice services and handlers.
// Extracted from Init to reduce cognitive complexity.
func (c *AccessCore) initSlices() error {
	// session-login must be constructed before identity-manage because
	// ChangePassword injects loginSvc as the TokenIssuer.
	loginOpts := []sessionlogin.Option{sessionlogin.WithEmitter(c.emitter), sessionlogin.WithTxManager(c.txRunner)}
	loginSvc := sessionlogin.NewService(c.userRepo, c.sessionRepo, c.roleRepo, c.jwtIssuer, c.logger, loginOpts...)
	c.loginHandler = sessionlogin.NewHandler(loginSvc)
	c.AddSlice(cell.NewBaseSlice("sessionlogin", "accesscore", cell.L2))

	// identity-manage: inject loginSvc as TokenIssuer for ChangePassword.
	identityOpts := []identitymanage.Option{identitymanage.WithEmitter(c.emitter), identitymanage.WithTxManager(c.txRunner)}
	identityOpts = append(identityOpts, identitymanage.WithTokenIssuer(loginSvc))
	identitySvc, err := identitymanage.NewService(c.userRepo, c.sessionRepo, c.logger, identityOpts...)
	if err != nil {
		return err
	}
	c.identityHandler = identitymanage.NewHandler(identitySvc)
	c.AddSlice(cell.NewBaseSlice("identitymanage", "accesscore", cell.L1))

	// session-validate (before session-refresh: provides session-aware verifier)
	c.validateSvc = sessionvalidate.NewService(c.jwtVerifier, c.sessionRepo, c.logger)
	c.AddSlice(cell.NewBaseSlice("sessionvalidate", "accesscore", cell.L0))

	// session-refresh uses jwtVerifier directly (not validateSvc) because
	// validateSvc hard-requires token_use=access and would reject every
	// refresh token. sessionrefresh still enforces session revocation via
	// sessionRepo + Session.IsRevoked checks after JWT verification.
	// WithUserRepository is injected so Refresh can read PasswordResetRequired
	// from the current user state (e.g. after ChangePassword clears the flag).
	refreshSvc := sessionrefresh.NewService(c.sessionRepo, c.roleRepo, c.userRepo, c.jwtIssuer, c.jwtVerifier, c.logger)
	c.refreshHandler = sessionrefresh.NewHandler(refreshSvc)
	c.AddSlice(cell.NewBaseSlice("sessionrefresh", "accesscore", cell.L1))

	// session-logout
	logoutOpts := []sessionlogout.Option{sessionlogout.WithEmitter(c.emitter), sessionlogout.WithTxManager(c.txRunner)}
	logoutSvc := sessionlogout.NewService(c.sessionRepo, c.logger, logoutOpts...)
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

	// config-receive: subscribes to config.changed events from configcore
	c.configReceiveSvc = configreceive.NewService(c.logger)
	c.AddSlice(cell.NewBaseSlice("configreceive", "accesscore", cell.L3))
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
	if c.initialAdmin == nil {
		return nil
	}
	return []cell.LifecycleHook{c.initialAdmin.Hook()}
}
