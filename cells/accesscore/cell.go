// Package accesscore implements the accesscore Cell: identity management,
// session lifecycle (login/refresh/logout/validate), RBAC authorization,
// and role queries.
package accesscore

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/cells/accesscore/internal/dto"
	"github.com/ghbvf/gocell/cells/accesscore/internal/initialadmin"
	"github.com/ghbvf/gocell/cells/accesscore/internal/mem"
	"github.com/ghbvf/gocell/cells/accesscore/internal/ports"
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
	"github.com/ghbvf/gocell/runtime/auth"
)

// Compile-time interface checks.
var (
	_ cell.Cell                 = (*AccessCore)(nil)
	_ cell.HTTPRegistrar        = (*AccessCore)(nil)
	_ cell.HealthContributor    = (*AccessCore)(nil)
	_ cell.EventRegistrar       = (*AccessCore)(nil)
	_ cell.LifecycleContributor = (*AccessCore)(nil)
)

// Option configures an AccessCore Cell.
type Option func(*AccessCore)

// WithUserRepository sets the UserRepository.
func WithUserRepository(r ports.UserRepository) Option {
	return func(c *AccessCore) { c.userRepo = r }
}

// WithSessionRepository sets the SessionRepository.
func WithSessionRepository(r ports.SessionRepository) Option {
	return func(c *AccessCore) { c.sessionRepo = r }
}

// WithRoleRepository sets the RoleRepository.
func WithRoleRepository(r ports.RoleRepository) Option {
	return func(c *AccessCore) { c.roleRepo = r }
}

// WithPublisher sets the outbox Publisher.
func WithPublisher(p outbox.Publisher) Option {
	return func(c *AccessCore) { c.publisher = p }
}

// WithLogger sets the structured logger.
func WithLogger(l *slog.Logger) Option {
	return func(c *AccessCore) { c.logger = l }
}

// WithJWTIssuer sets the RS256 JWT issuer for token signing.
func WithJWTIssuer(issuer *auth.JWTIssuer) Option {
	return func(c *AccessCore) { c.jwtIssuer = issuer }
}

// WithJWTVerifier sets the RS256 JWT verifier for token validation.
func WithJWTVerifier(verifier *auth.JWTVerifier) Option {
	return func(c *AccessCore) { c.jwtVerifier = verifier }
}

// WithOutboxWriter sets the outbox.Writer for transactional event publishing.
func WithOutboxWriter(w outbox.Writer) Option {
	return func(c *AccessCore) { c.outboxWriter = w }
}

// WithCursorCodec sets the cursor codec for pagination. Required in durable mode.
func WithCursorCodec(codec *query.CursorCodec) Option {
	return func(c *AccessCore) { c.cursorCodec = codec }
}

// WithTxManager sets the TxRunner for transactional guarantees (L2 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(c *AccessCore) { c.txRunner = tx }
}

// ResolveBootstrapCredentialPath returns the credential file path using the
// same resolution logic as the internal Bootstrapper: stateDir overrides
// GOCELL_STATE_DIR, which overrides the default /run/gocell path.
//
// This is the canonical path helper for cmd/corebundle startup logging so
// that the logged path always matches the file the bootstrapper writes (P2-6).
func ResolveBootstrapCredentialPath(stateDir string) (string, error) {
	return initialadmin.ResolveCredentialPath(stateDir)
}

// WithInMemoryDefaults configures in-memory repositories for development
// and testing. Not suitable for production use.
func WithInMemoryDefaults() Option {
	return func(c *AccessCore) {
		c.userRepo = mem.NewUserRepository()
		c.sessionRepo = mem.NewSessionRepository()
		c.roleRepo = mem.NewRoleRepository()
	}
}

// WithInitialAdminBootstrap enables first-run admin bootstrap (scheme H).
// Bootstrap auto-discovers the returned Lifecycle via cell.LifecycleContributor
// (kernel/cell.LifecycleContributor → runtime/bootstrap phase3b) and wires
// OnStart/OnStop — no composition-root plumbing required.
//
// ref: docs/architecture/202604181900-adr-auth-setup-first-run.md (scheme H)
func WithInitialAdminBootstrap(opts ...initialadmin.LifecycleOption) Option {
	return func(c *AccessCore) { c.initialAdmin = initialadmin.NewLifecycle(opts...) }
}

// AccessCore is the accesscore Cell implementation.
type AccessCore struct {
	*cell.BaseCell
	userRepo     ports.UserRepository
	sessionRepo  ports.SessionRepository
	roleRepo     ports.RoleRepository
	publisher    outbox.Publisher
	outboxWriter outbox.Writer
	txRunner     persistence.TxRunner
	emitter      outbox.Emitter
	logger       *slog.Logger
	jwtIssuer    *auth.JWTIssuer
	jwtVerifier  *auth.JWTVerifier
	cursorCodec  *query.CursorCodec

	// initialAdmin wires first-run admin bootstrap via LifecycleContributor;
	// nil means the feature is disabled.
	initialAdmin *initialadmin.Lifecycle

	// Slice handlers.
	identityHandler *identitymanage.Handler
	loginHandler    *sessionlogin.Handler
	refreshHandler  *sessionrefresh.Handler
	logoutHandler   *sessionlogout.Handler

	// Services exposed for composition (e.g. TokenVerifier, Authorizer).
	validateSvc         *sessionvalidate.Service
	authzSvc            *authorizationdecide.Service
	rbacHandler         *rbaccheck.Handler
	rbacRunMode         query.RunMode
	rbacEmitterMode     bool
	rbacAssignHandler   *rbacassign.Handler
	configReceiveSvc    *configreceive.Service
	rbacSessionConsumer *sessionlogout.Consumer
}

// NewAccessCore creates a new AccessCore Cell.
func NewAccessCore(opts ...Option) *AccessCore {
	c := &AccessCore{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{
			ID:               "accesscore",
			Type:             cell.CellTypeCore,
			ConsistencyLevel: cell.L2,
			Owner:            cell.Owner{Team: "platform", Role: "access-owner"},
			Schema:           cell.SchemaConfig{Primary: "users"},
			Verify:           cell.CellVerify{Smoke: []string{"accesscore/smoke"}},
		}),
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// HealthCheckers implements cell.HealthContributor. Returns named readiness
// probes for internal components. Bootstrap auto-discovers this interface
// and registers probes in /readyz.
//
// Currently exposes "session-store" when the session repo implements
// ports.HealthCheckable. Both in-memory and real adapters implement
// HealthCheckable, so the probe is present in all modes. Returns an
// empty map only when sessionRepo is nil (no repo injected at all).
func (c *AccessCore) HealthCheckers() map[string]func(context.Context) error {
	checkers := make(map[string]func(context.Context) error)
	if hc, ok := c.sessionRepo.(ports.HealthCheckable); ok {
		checkers["session-store"] = func(ctx context.Context) error {
			return hc.Health(ctx)
		}
	}
	return checkers
}

// TokenVerifier returns the session-validate service. It satisfies
// auth.IntentTokenVerifier so it can be plugged into AuthMiddleware without
// a runtime type assertion.
func (c *AccessCore) TokenVerifier() auth.IntentTokenVerifier {
	if c.validateSvc == nil {
		return nil
	}
	return c.validateSvc
}

// Authorizer returns the authorization-decide service (implements auth.Authorizer).
func (c *AccessCore) Authorizer() auth.Authorizer {
	return c.authzSvc
}

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

// Init constructs all 9 slices.
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
	// Bind initialAdmin after slices so repos are ready. Bootstrap auto-discovers
	// LifecycleHooks() and calls OnStart/OnStop — no WorkerSink plumbing needed.
	if c.initialAdmin != nil {
		c.initialAdmin.Bind(initialadmin.BootstrapDeps{
			UserRepo: c.userRepo,
			RoleRepo: c.roleRepo,
			Logger:   c.logger,
			Clock:    nil, // Lifecycle defaults Clock from its cfg; no per-cell override needed
		}, c.logger)
	}
	return nil
}

// LifecycleHooks implements cell.LifecycleContributor. Returns the initial-admin
// hook when WithInitialAdminBootstrap was applied; nil otherwise (opt-out).
//
// Bootstrap phase3b auto-discovers this interface and appends the returned hooks
// to the Lifecycle — eliminating the old WithBootstrapWorkerSink composition-root
// plumbing.
func (c *AccessCore) LifecycleHooks() []cell.LifecycleHook {
	if c.initialAdmin == nil {
		return nil
	}
	return []cell.LifecycleHook{c.initialAdmin.Hook()}
}

// RegisterRoutes registers HTTP routes for accesscore.
func (c *AccessCore) RegisterRoutes(mux cell.RouteMux) {
	mux.Route("/api/v1/access", func(sub cell.RouteMux) {
		// Identity management: /api/v1/access/users
		sub.Route("/users", c.identityHandler.RegisterRoutes)

		// Session endpoints: /api/v1/access/sessions.
		// Public routes, password-reset-exempt routes and their implicit hint are
		// all declared inline here. Router.FinalizeAuth aggregates every Cell's
		// declarations at Bootstrap phase 5.
		// Login and refresh are public (no JWT required). Logout requires the
		// caller to be authenticated as the session owner or an admin, and is
		// PasswordResetExempt so a token carrying password_reset_required=true
		// can still reach this endpoint.
		sub.Route("/sessions", func(s cell.RouteMux) {
			auth.Declare(s, auth.RouteDecl{
				Method:  "POST",
				Path:    "/login",
				Handler: http.HandlerFunc(c.loginHandler.HandleLogin),
				Public:  true,
			})
			auth.Declare(s, auth.RouteDecl{
				Method:  "POST",
				Path:    "/refresh",
				Handler: http.HandlerFunc(c.refreshHandler.HandleRefresh),
				Public:  true,
			})
			// Logout: {id} is a session id, NOT a user id, so the route-level
			// policy cannot be SelfOr("id", admin). Session ownership is enforced
			// inside HandleLogout by comparing the principal subject against the
			// session's user_id. Baseline AuthMiddleware still requires a valid
			// JWT; PasswordResetExempt keeps the route reachable while the caller
			// still owes a password reset (standard user-self-recovery flow).
			auth.Declare(s, auth.RouteDecl{
				Method:              "DELETE",
				Path:                "/{id}",
				Handler:             http.HandlerFunc(c.logoutHandler.HandleLogout),
				PasswordResetExempt: true,
			})
		})

		// RBAC queries: /api/v1/access/roles
		sub.Route("/roles", c.rbacHandler.RegisterRoutes)
	})

	// Internal admin endpoints: /internal/v1/access/roles
	mux.Route("/internal/v1/access", func(sub cell.RouteMux) {
		sub.Route("/roles", c.rbacAssignHandler.RegisterRoutes)
	})
}

// RegisterSubscriptions declares event subscriptions for accesscore.
// The Router manages goroutine lifecycle and setup-error detection.
func (c *AccessCore) RegisterSubscriptions(r cell.EventRouter) error {
	// config-receive: config.changed events from configcore.
	handler := outbox.WrapLegacyHandler(c.configReceiveSvc.HandleEvent)
	r.AddHandler(configreceive.TopicConfigChanged, handler, "accesscore")

	// rbac-session-sync: invalidate sessions on role assignment or revocation.
	// Both topics share the same handler and consumer group — HandleRoleChanged is topic-agnostic.
	roleHandler := outbox.WrapLegacyHandler(c.rbacSessionConsumer.HandleRoleChanged)
	r.AddHandler(dto.TopicRoleAssigned, roleHandler, "accesscore-rbac-session-sync")
	r.AddHandler(dto.TopicRoleRevoked, roleHandler, "accesscore-rbac-session-sync")
	return nil
}
