package accesscore

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/google/uuid"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/accountlockout"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/adminprovision"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/authzmutate"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/credentialinvalidate"
	"github.com/ghbvf/gocell/corecells/accesscore/slices/authorizationdecide"
	"github.com/ghbvf/gocell/corecells/accesscore/slices/configreceive"
	"github.com/ghbvf/gocell/corecells/accesscore/slices/identitymanage"
	"github.com/ghbvf/gocell/corecells/accesscore/slices/policymanage"
	"github.com/ghbvf/gocell/corecells/accesscore/slices/rbacassign"
	"github.com/ghbvf/gocell/corecells/accesscore/slices/rbaccheck"
	"github.com/ghbvf/gocell/corecells/accesscore/slices/sessionlogin"
	"github.com/ghbvf/gocell/corecells/accesscore/slices/sessionlogout"
	"github.com/ghbvf/gocell/corecells/accesscore/slices/sessionrefresh"
	"github.com/ghbvf/gocell/corecells/accesscore/slices/sessionvalidate"
	"github.com/ghbvf/gocell/corecells/accesscore/slices/setup"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/healthz"
	"github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/pkg/errcode"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/validation"
	"github.com/ghbvf/gocell/framework/runtime/auth/refresh"
)

// resolveEmitter delegates to outbox.ResolveCellEmitter (mutual exclusion +
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
func (c *AccessCore) resolveEmitter(mode outbox.DurabilityMode) error {
	txRunnerForEmitter := c.txRunner
	if c.pendingOutboxWriter == nil {
		txRunnerForEmitter = nil
	}
	resolved, err := outbox.ResolveCellEmitter(c.clk, outbox.CellEmitterInputs{
		EmitterConfig: outbox.EmitterConfig{
			CellID:            "accesscore",
			Mode:              mode,
			Publisher:         c.pendingOutboxPub,
			OutboxWriter:      c.pendingOutboxWriter,
			TxRunner:          txRunnerForEmitter,
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
	c.emitter = resolved
	c.pendingOutboxPub = nil
	c.pendingOutboxWriter = nil
	return nil
}

// initValidate performs fail-fast validation of required dependencies before
// constructing slices. Extracted from Init to reduce cognitive complexity.
func (c *AccessCore) initValidate(durabilityMode outbox.DurabilityMode) error {
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
		if durabilityMode == outbox.DurabilityDurable {
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
	c.listRunMode = query.RunModeForDemo(durabilityMode == outbox.DurabilityDemo)
	// resolveEmitter (called above) enforces the (OutboxWriter, TxRunner)
	// pairing invariant using the original c.txRunner; only after it
	// succeeds do we install the demoTxRunner fallback so slice constructors
	// see a non-nil TxRunner.
	if c.txRunner == nil {
		c.logger.Warn("accesscore: using outbox.DemoCellTxManager (demo mode)",
			slog.String("durability_mode", durabilityMode.String()))
		c.txRunner = outbox.DemoCellTxManager()
	}
	// Guard: DemoTxRunner implements Nooper — reject it in DurabilityDurable mode
	// so that assemblies that forget to wire a real TxRunner fail at Init() time.
	if err := outbox.CheckNotNoop(durabilityMode, "accesscore", c.txRunner); err != nil {
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
			"accesscore requires a user repository: wire WithMemBundle or WithPGBundle")
	}
	if c.sessionStoreNil || c.sessionStore == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore requires a session store: use WithSessionStore")
	}
	if c.roleRepo == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore requires a role repository: wire WithMemBundle or WithPGBundle")
	}
	if c.policyRepo == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore requires a policy repository: wire WithMemBundle or WithPGBundle")
	}
	if c.resourceAttrs == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore requires a resource attribute provider: wire WithMemBundle or WithPGBundle")
	}
	if c.refreshStore == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellMissingTokenIssuer,
			"refresh.Store required: use WithRefreshStore")
	}
	if c.casProtocol == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore: WithCASProtocol is required for ChangePassword concurrent-write guard (S6); "+
				"composition root must wire cas.NewProtocol(cas.WithVersionField(\"password_version\")) "+
				"via WithCASProtocol")
	}
	if c.bootstrapAuth == nil {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore: WithBootstrapAuth is required (auth.bootstrap:true contracts "+
				"need a per-route replacement authenticator; composition root must wire "+
				"runtime/auth.NewBootstrapMiddleware via WithBootstrapAuth). "+
				"See docs/architecture/202605061600-adr-bootstrap-admin-boundary.md §D1.")
	}
	if c.setupLockNil || validation.IsNilInterface(c.setupLock) {
		return errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"accesscore: setupLock is required for admin-provisioning serialization; "+
				"wire WithMemBundle (NoopSetupLock — memTxRunner.RunInTx serializes via store.mu) "+
				"or WithPGBundle (pg_advisory_xact_lock across pods). The previous in-process "+
				"sync.Mutex inside adminprovision.Provisioner has been removed.")
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
// constraints (login before identity, accountlockout before login, etc.) outweigh
// the funlen / cognitive-complexity budgets.
//
//nolint:funlen // sequential cell composition root; readability and ordering
func (c *AccessCore) initSlices() error {
	// credentialinvalidate: shared invalidator for identity-manage, rbac-assign,
	// session-refresh, and accountlockout. Atomically bumps authz_epoch, revokes
	// all sessions, and revokes all refresh tokens for a subject when a
	// credential-invalidating event (password change, role assignment, token
	// reuse, account auto-lock) is detected. Built first so accountlockout can
	// route through it via authzmutate.
	inv, err := credentialinvalidate.New(c.userRepo, c.sessionStore, c.refreshStore)
	if err != nil {
		return err
	}
	c.invalidator = inv

	// accountlockout: typed mediator for sessionlogin auto-lockout decisions.
	// Owns the failure-window policy + persists counter via UserRepository +
	// routes lock/unlock through authzmutate. sessionlogin imports this package
	// instead of authzmutate directly (depguard upstream Hard funnel
	// SESSIONLOGIN-LOCKOUT-VIA-ACCOUNTLOCKOUT-01).
	lockoutSvc, err := c.initAccountLockout()
	if err != nil {
		return err
	}

	// session-login must be constructed before identity-manage because
	// ChangePassword injects loginSvc as the TokenIssuer.
	loginOpts := []sessionlogin.Option{
		sessionlogin.WithEmitter(c.emitter),
		sessionlogin.WithTxManager(c.txRunner),
		sessionlogin.WithSessionTTL(DefaultRefreshMaxAge),
		sessionlogin.WithAccountLockout(lockoutSvc),
	}
	loginSvc, err := sessionlogin.NewService(
		c.clk,
		sessionlogin.NewServiceParams{
			UserRepo:     c.userRepo,
			SessionStore: c.sessionStore,
			RoleRepo:     c.roleRepo,
			RefreshStore: c.refreshStore,
			Issuer:       c.jwtIssuer,
		},
		c.logger,
		loginOpts...,
	)
	if err != nil {
		return err
	}
	c.loginHandler = sessionlogin.NewHandler(loginSvc, DefaultRefreshMaxAge)
	c.AddSlice(cell.MustNewBaseSliceFromMeta(sessionlogin.SliceMetadata()))

	// identity-manage: inject loginSvc as TokenIssuer for ChangePassword.
	identityOpts := []identitymanage.Option{
		identitymanage.WithEmitter(c.emitter),
		identitymanage.WithTxManager(c.txRunner),
		identitymanage.WithPasswordHasher(c.passwordHasher),
	}
	identityOpts = append(identityOpts, identitymanage.WithTokenIssuer(loginSvc))
	identitySvc, err := identitymanage.NewService(c.clk, c.userRepo, c.invalidator, c.logger, c.roleRepo, identityOpts...)
	if err != nil {
		return err
	}
	c.identityHandler = identitymanage.NewHandler(identitySvc)
	c.AddSlice(cell.MustNewBaseSliceFromMeta(identitymanage.SliceMetadata()))

	// session-validate (before session-refresh: provides session-aware verifier)
	validateSvc, err := sessionvalidate.NewService(c.jwtVerifier, c.sessionStore, c.userRepo, c.logger,
		sessionvalidate.WithTxManager(c.txRunner))
	if err != nil {
		return err
	}
	c.validateSvc = validateSvc
	c.AddSlice(cell.MustNewBaseSliceFromMeta(sessionvalidate.SliceMetadata()))

	// session-refresh uses refresh.Store for token state validation and
	// rotation. No JWT verifier is needed — the opaque wire format is
	// validated by the store itself; any malformed input (including an
	// access JWT replay attempt) returns ErrRejected.
	refreshSvc, err := sessionrefresh.NewService(
		c.clk,
		sessionrefresh.NewServiceParams{
			SessionStore: c.sessionStore,
			RoleRepo:     c.roleRepo,
			UserRepo:     c.userRepo,
			RefreshStore: c.refreshStore,
			Issuer:       c.jwtIssuer,
		},
		c.logger,
		sessionrefresh.WithTxManager(c.txRunner),
		sessionrefresh.WithInvalidator(c.invalidator),
	)
	if err != nil {
		return err
	}
	c.refreshHandler = sessionrefresh.NewHandler(refreshSvc, DefaultRefreshMaxAge)
	c.AddSlice(cell.MustNewBaseSliceFromMeta(sessionrefresh.SliceMetadata()))

	// session-logout — cascades revocation to refresh.Store so logout
	// invalidates the full refresh chain, not just the access session.
	logoutOpts := []sessionlogout.Option{sessionlogout.WithEmitter(c.emitter), sessionlogout.WithTxManager(c.txRunner)}
	logoutSvc, err := sessionlogout.NewService(c.clk, c.sessionStore, c.refreshStore, c.logger, logoutOpts...)
	if err != nil {
		return err
	}
	c.logoutHandler = sessionlogout.NewHandler(logoutSvc, DefaultRefreshMaxAge)
	c.AddSlice(cell.MustNewBaseSliceFromMeta(sessionlogout.SliceMetadata()))

	// authorization-decide (ABAC PDP engine, #1345 PR-7). The policy store is
	// wired through the bundle funnel (#1346 PR-8): WithMemBundle supplies the
	// in-memory PolicyRepository, WithPGBundle the durable PG-backed one. Both are
	// fail-fast at the deps preflight above (c.policyRepo != nil).
	authzSvc, err := authorizationdecide.NewService(c.clk, c.policyRepo, c.resourceAttrs, c.logger,
		authorizationdecide.WithTxManager(c.txRunner))
	if err != nil {
		return err
	}
	c.authzSvc = authzSvc
	c.AddSlice(cell.MustNewBaseSliceFromMeta(authorizationdecide.SliceMetadata()))

	// policymanage: L2 OutboxFact CRUD for ABAC policies (#1347 PR-9).
	// Emits event.policy.updated.v1 atomically on every create/update/delete.
	if err := c.initPolicyManageSlice(); err != nil {
		return err
	}

	// rbac-check
	rbacSvc, err := rbaccheck.NewService(c.roleRepo, c.cursorCodec, c.logger, c.listRunMode,
		rbaccheck.WithTxManager(c.txRunner))
	if err != nil {
		return err
	}
	c.rbacHandler = rbaccheck.NewHandler(rbacSvc)
	c.AddSlice(cell.MustNewBaseSliceFromMeta(rbaccheck.SliceMetadata()))

	// rbac-assign is always L2 OutboxFact — the declaration lives in
	// corecells/accesscore/slices/rbacassign/slice.yaml (the SoR projected by
	// codegen into slice_gen.go.sliceMeta). The constructor consumes the
	// metadata through cell.MustNewBaseSliceFromMeta; runtime emit fidelity
	// depends on resolveEmitter output — see initRbacAssign godoc.
	if err := c.initRbacAssign(); err != nil {
		return err
	}

	// rbac-session-sync consumer: handles role-change events and invalidates sessions.
	c.rbacSessionConsumer = sessionlogout.NewConsumer(c.logger)

	// config-receive: subscribes to config state-sync events from configcore.
	// WithConfigGetter is optional — nil disables the cross-cell GetEntry fetch.
	c.configReceiveSvc = configreceive.NewService(
		c.logger,
		configreceive.WithConfigGetter(c.configGetter),
		configreceive.WithConfigEventCollector(c.configEventCollector),
	)
	c.AddSlice(cell.MustNewBaseSliceFromMeta(configreceive.SliceMetadata()))

	// setup: first-run admin provisioning.
	// Uses shared adminprovision.Provisioner so semantics match initialadmin.
	// casProtocol / bootstrapAuth / setupLock required-dep checks are
	// enforced by validateRequiredDeps (phase0); they have already passed
	// when execution reaches here.
	setupProv, err := adminprovision.NewProvisioner(c.userRepo, c.roleRepo, c.logger, uuid.NewString, c.clk)
	if err != nil {
		return err
	}
	setupSvc, err := setup.NewService(
		c.clk, setupProv, c.logger,
		setup.WithEmitter(c.emitter),
		setup.WithTxManager(c.txRunner),
		setup.WithSetupLock(c.setupLock),
		setup.WithPasswordHasher(c.passwordHasher),
	)
	if err != nil {
		return err
	}
	c.setupSvc = setupSvc // stored for RecordBootstrapAuthFail (Wave-1 #1423)
	c.setupHandler = setup.NewHandler(setupSvc, c.bootstrapAuth)
	c.AddSlice(cell.MustNewBaseSliceFromMeta(setup.SliceMetadata()))
	return nil
}

// initRbacAssign constructs the rbac-assign slice. rbacassign is L2 OutboxFact:
// the slice's behavioral contract is to emit role.assigned / role.revoked
// outbox facts atomically inside RunInTx. The consistency level is the typed
// projection of `corecells/accesscore/slices/rbacassign/slice.yaml` —
// `cell.MustNewBaseSliceFromMeta(rbacassign.SliceMetadata())` reads
// `consistencyLevel: L2` from the codegen literal, independent of runtime mode.
//
// Runtime emit fidelity depends on outbox.ResolveCellEmitter's output:
//   - durable mode (publisher + writer + txRunner) → WriterEmitter writes a row
//     in the outbox table; the row + role write co-commit, providing real L2
//     atomicity end-to-end.
//   - publisher-only demo (no writer) → DirectEmitter synchronously publishes
//     without a durable outbox row. The slice still drives the funnel
//     (RunInTx → emit) but there is no row to replay on failure — this is
//     test/demo fidelity only, not L2 atomicity.
func (c *AccessCore) initRbacAssign() error {
	rbacAssignSvc, err := rbacassign.NewService(
		c.clk, c.roleRepo, c.userRepo, c.invalidator, c.logger,
		rbacassign.WithEmitter(c.emitter),
		rbacassign.WithTxManager(c.txRunner),
	)
	if err != nil {
		return err
	}
	c.rbacAssignHandler = rbacassign.NewHandler(rbacAssignSvc)
	c.AddSlice(cell.MustNewBaseSliceFromMeta(rbacassign.SliceMetadata()))
	return nil
}

// initAccountLockout builds the accountlockout mediator: the typed lock/unlock
// service that sessionlogin routes auto-lockout decisions through. Requires
// c.invalidator (built earlier in initSlices). Extracted from initSlices to keep
// that function's cognitive complexity within budget.
func (c *AccessCore) initAccountLockout() (*accountlockout.Service, error) {
	lockoutMutator, err := authzmutate.New(c.invalidator, c.userRepo)
	if err != nil {
		return nil, fmt.Errorf("accesscore: build lockout authzmutator: %w", err)
	}
	lockoutOpts := []accountlockout.Option{}
	if c.lockoutMetrics != nil {
		lockoutOpts = append(lockoutOpts, accountlockout.WithMetrics(c.lockoutMetrics))
	}
	if c.logger != nil {
		lockoutOpts = append(lockoutOpts, accountlockout.WithLogger(c.logger))
	}
	lockoutSvc, err := accountlockout.NewService(c.userRepo, lockoutMutator, c.emitter, c.clk, lockoutOpts...)
	if err != nil {
		return nil, fmt.Errorf("accesscore: build accountlockout service: %w", err)
	}
	return lockoutSvc, nil
}

// initPolicyManageSlice constructs the policymanage slice. policymanage is L2
// OutboxFact: every policy mutation (Create/Update/Delete) atomically co-commits
// an event.policy.updated.v1 outbox row inside RunInTx. Runtime emit fidelity
// depends on outbox.ResolveCellEmitter output — same as initRbacAssign.
func (c *AccessCore) initPolicyManageSlice() error {
	pmSvc, err := policymanage.NewService(
		c.clk, c.policyRepo, c.cursorCodec, c.logger, c.listRunMode,
		policymanage.WithEmitter(c.emitter),
		policymanage.WithTxManager(c.txRunner),
	)
	if err != nil {
		return fmt.Errorf("accesscore: build policymanage service: %w", err)
	}
	c.policyHandler = policymanage.NewHandler(pmSvc)
	c.AddSlice(cell.MustNewBaseSliceFromMeta(policymanage.SliceMetadata()))
	return nil
}

// initInternal is the K#04 codegen escape hatch: business init that cannot
// be generated (emitter resolve, slice service construction, health probes,
// lifecycle hooks). cell_gen.go::Init calls it after BaseCell.Init and before
// mounting the generated route-group and subscribe blocks. This is a permanent
// convention, not a transitional shim.
//
//nolint:unparam // ctx is part of the K#04 initInternal contract; unused here, used by other cells (devicecell)
func (c *AccessCore) initInternal(ctx context.Context, reg cell.Registrar) error {
	durabilityMode := reg.DurabilityMode()

	if err := c.initValidate(durabilityMode); err != nil {
		return err
	}
	if err := c.initSlices(); err != nil {
		return err
	}

	// Route groups and subscriptions removed: cell_gen.go owns Init and renders them.
	if err := c.registerHealthAndLifecycle(reg); err != nil {
		return err
	}

	return nil
}

// repoReadyAll aggregates several healthz.RepoProber values into one cell-level
// readiness signal: ready only when EVERY backing repo is ready (fail-closed —
// the first not-ready repo's error is returned). cellgen emits exactly one
// readiness probe per cell (accesscore_repo_ready), so a cell with multiple
// independently-failable stores folds them through this composite rather than
// minting per-repo probes (#1346 PR-8, T8.4 — observability.md "cell repo
// readiness 由 cell 边界显式注册，禁止静默吞掉缺失 repo").
type repoReadyAll []healthz.RepoProber

func (rs repoReadyAll) RepoReady(ctx context.Context) error {
	for _, r := range rs {
		if err := r.RepoReady(ctx); err != nil {
			return err
		}
	}
	return nil
}

// registerHealthAndLifecycle registers health probes and lifecycle hooks into reg.
func (c *AccessCore) registerHealthAndLifecycle(reg cell.Registrar) error {
	// Cell-level readiness aggregates every independently-failable repo: the
	// session store and (since #1346 PR-8) the ABAC policy store. Both satisfy
	// healthz.RepoProber via RepoReady; the composite is registered through the
	// cellgen-generated RegisterReadiness funnel as accesscore_repo_ready.
	if err := RegisterReadiness(reg, repoReadyAll{c.sessionStore, c.policyRepo}); err != nil {
		return err
	}
	if err := cell.RegisterEmitterHealthProbes(reg, c.emitter); err != nil {
		return err
	}
	if c.refreshGCEnabled {
		reg.Lifecycle(c.refreshGCHook())
	}
	return nil
}
