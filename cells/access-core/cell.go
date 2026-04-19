// Package accesscore implements the access-core Cell: identity management,
// session lifecycle (login/refresh/logout/validate), RBAC authorization,
// and role queries.
package accesscore

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/cells/access-core/internal/dto"
	"github.com/ghbvf/gocell/cells/access-core/internal/initialadmin"
	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/cells/access-core/slices/authorizationdecide"
	"github.com/ghbvf/gocell/cells/access-core/slices/configreceive"
	"github.com/ghbvf/gocell/cells/access-core/slices/identitymanage"
	"github.com/ghbvf/gocell/cells/access-core/slices/rbacassign"
	"github.com/ghbvf/gocell/cells/access-core/slices/rbaccheck"
	"github.com/ghbvf/gocell/cells/access-core/slices/sessionlogin"
	"github.com/ghbvf/gocell/cells/access-core/slices/sessionlogout"
	"github.com/ghbvf/gocell/cells/access-core/slices/sessionrefresh"
	"github.com/ghbvf/gocell/cells/access-core/slices/sessionvalidate"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/worker"
)

// Compile-time interface checks.
var (
	_ cell.Cell              = (*AccessCore)(nil)
	_ cell.HTTPRegistrar     = (*AccessCore)(nil)
	_ cell.HealthContributor = (*AccessCore)(nil)
	_ cell.EventRegistrar    = (*AccessCore)(nil)
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

// WithTxManager sets the TxRunner for transactional guarantees (L2 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(c *AccessCore) { c.txRunner = tx }
}

// ResolveBootstrapCredentialPath returns the credential file path using the
// same resolution logic as the internal Bootstrapper: stateDir overrides
// GOCELL_STATE_DIR, which overrides the default /run/gocell path.
//
// This is the canonical path helper for cmd/core-bundle startup logging so
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

// initialAdminConfig holds the parsed options for WithInitialAdminBootstrap.
type initialAdminConfig struct {
	username       string
	credentialPath string
	ttl            time.Duration
	passwordSource io.Reader // nil → crypto/rand.Reader (default in GeneratePassword)
	scheduler      initialadmin.Scheduler
	clock          initialadmin.Clock
}

// InitialAdminOption configures WithInitialAdminBootstrap.
type InitialAdminOption func(*initialAdminConfig)

// WithBootstrapUsername overrides the admin username (default: "admin").
func WithBootstrapUsername(u string) InitialAdminOption {
	return func(c *initialAdminConfig) { c.username = u }
}

// WithBootstrapCredentialPath overrides the credential file path.
// Default resolution: GOCELL_STATE_DIR/initial_admin_password →
// /run/gocell/initial_admin_password.
func WithBootstrapCredentialPath(p string) InitialAdminOption {
	return func(c *initialAdminConfig) { c.credentialPath = p }
}

// WithBootstrapTTL overrides the credential file TTL (default: 24h).
func WithBootstrapTTL(d time.Duration) InitialAdminOption {
	return func(c *initialAdminConfig) { c.ttl = d }
}

// withBootstrapPasswordSource is an unexported helper used by tests to inject
// a deterministic password source. Production code always uses the default
// crypto/rand.Reader via GeneratePassword.
func withBootstrapPasswordSource(r io.Reader) InitialAdminOption {
	return func(c *initialAdminConfig) { c.passwordSource = r }
}

// WithInitialAdminBootstrap enables first-run admin bootstrap (scheme H).
//
// During Init, if no user holds the admin role, a random password is generated,
// written to a credential file (mode 0600), and the returned cleanup worker
// removes the file after the configured TTL.
//
// Must be paired with WithBootstrapWorkerSink so the cleanup worker is handed
// to a lifecycle manager (e.g., bootstrap.WithWorkers). Init fails fast if the
// sink is missing.
//
// ref: docs/architecture/202604181900-adr-auth-setup-first-run.md (scheme H)
func WithInitialAdminBootstrap(opts ...InitialAdminOption) Option {
	cfg := &initialAdminConfig{}
	for _, o := range opts {
		o(cfg)
	}
	return func(c *AccessCore) { c.initialAdminCfg = cfg }
}

// WithBootstrapWorkerSink injects a sink function that receives the cleanup
// worker produced by the bootstrap sequence. The caller must hand this worker
// to a lifecycle manager (e.g., bootstrap.WithWorkers). A non-nil sink is
// required whenever WithInitialAdminBootstrap is used.
func WithBootstrapWorkerSink(sink func(worker.Worker)) Option {
	return func(c *AccessCore) { c.bootstrapWorkerSink = sink }
}

// AccessCore is the access-core Cell implementation.
type AccessCore struct {
	*cell.BaseCell
	userRepo     ports.UserRepository
	sessionRepo  ports.SessionRepository
	roleRepo     ports.RoleRepository
	publisher    outbox.Publisher
	outboxWriter outbox.Writer
	txRunner     persistence.TxRunner
	logger       *slog.Logger
	jwtIssuer    *auth.JWTIssuer
	jwtVerifier  *auth.JWTVerifier

	// Bootstrap configuration (set via WithInitialAdminBootstrap).
	initialAdminCfg     *initialAdminConfig
	bootstrapWorkerSink func(worker.Worker)

	// Slice handlers.
	identityHandler *identitymanage.Handler
	loginHandler    *sessionlogin.Handler
	refreshHandler  *sessionrefresh.Handler
	logoutHandler   *sessionlogout.Handler

	// Services exposed for composition (e.g. TokenVerifier, Authorizer).
	validateSvc         *sessionvalidate.Service
	authzSvc            *authorizationdecide.Service
	rbacHandler         *rbaccheck.Handler
	rbacAssignHandler   *rbacassign.Handler
	configReceiveSvc    *configreceive.Service
	rbacSessionConsumer *sessionlogout.Consumer
}

// NewAccessCore creates a new AccessCore Cell.
func NewAccessCore(opts ...Option) *AccessCore {
	c := &AccessCore{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{
			ID:               "access-core",
			Type:             cell.CellTypeCore,
			ConsistencyLevel: cell.L2,
			Owner:            cell.Owner{Team: "platform", Role: "access-owner"},
			Schema:           cell.SchemaConfig{Primary: "users"},
			Verify:           cell.CellVerify{Smoke: []string{"access-core/smoke"}},
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
func (c *AccessCore) HealthCheckers() map[string]func() error {
	checkers := make(map[string]func() error)
	if hc, ok := c.sessionRepo.(ports.HealthCheckable); ok {
		checkers["session-store"] = hc.Health
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

// runInitialAdminBootstrap executes the first-run admin bootstrap when
// WithInitialAdminBootstrap has been configured. It validates that
// WithBootstrapWorkerSink is also set, then delegates to Bootstrapper.Run.
// A no-op (nil cfg) means the bootstrap feature is disabled.
func (c *AccessCore) runInitialAdminBootstrap(ctx context.Context) error {
	if c.initialAdminCfg == nil {
		return nil
	}
	if c.bootstrapWorkerSink == nil {
		return errcode.New(errcode.ErrCellInvalidConfig,
			"WithInitialAdminBootstrap requires WithBootstrapWorkerSink; example:\n"+
				"  var w worker.Worker\n"+
				"  accesscore.WithInitialAdminBootstrap(),\n"+
				"  accesscore.WithBootstrapWorkerSink(func(x worker.Worker) { w = x }),\n"+
				"  /* later */ bootstrap.WithWorkers(w)")
	}
	bsDeps := initialadmin.BootstrapDeps{
		UserRepo: c.userRepo,
		RoleRepo: c.roleRepo,
		Logger:   c.logger,
		Clock:    c.initialAdminCfg.clock,
	}
	bsCfg := initialadmin.BootstrapConfig{
		Username:       c.initialAdminCfg.username,
		CredentialPath: c.initialAdminCfg.credentialPath,
		TTL:            c.initialAdminCfg.ttl,
		PasswordSource: c.initialAdminCfg.passwordSource,
		Scheduler:      c.initialAdminCfg.scheduler,
	}
	bs, err := initialadmin.NewBootstrapper(bsDeps, bsCfg)
	if err != nil {
		return fmt.Errorf("access-core: bootstrap construct: %w", err)
	}
	cleanerWorker, err := bs.Run(ctx)
	if err != nil {
		return fmt.Errorf("access-core: bootstrap admin: %w", err)
	}
	if cleanerWorker != nil {
		c.bootstrapWorkerSink(cleanerWorker)
	}
	return nil
}

// initValidate performs fail-fast validation of required dependencies before
// constructing slices. Extracted from Init to reduce cognitive complexity.
func (c *AccessCore) initValidate(deps cell.Dependencies) error {
	if (c.outboxWriter == nil) != (c.txRunner == nil) {
		return errcode.New(errcode.ErrCellMissingOutbox,
			"access-core durable mode requires both outboxWriter and txRunner")
	}
	if err := cell.CheckNotNoop(deps.DurabilityMode, "access-core", c.outboxWriter, c.txRunner, c.publisher); err != nil {
		return err
	}
	if c.outboxWriter == nil && c.txRunner == nil {
		if err := c.validateDemoMode(); err != nil {
			return err
		}
	}
	if c.jwtIssuer == nil || c.jwtVerifier == nil {
		return errcode.New(errcode.ErrAuthKeyInvalid,
			"RS256 key pair required: use WithJWTIssuer and WithJWTVerifier")
	}
	return nil
}

// validateDemoMode is called when both outboxWriter and txRunner are nil.
func (c *AccessCore) validateDemoMode() error {
	if c.publisher == nil {
		return errcode.New(errcode.ErrCellMissingOutbox,
			"access-core requires publisher or outbox writer; use WithPublisher(&outbox.DiscardPublisher{}) for demo mode")
	}
	if c.ConsistencyLevel() >= cell.L2 {
		c.logger.Warn("access-core: running without outboxWriter+txRunner, L2 transactional atomicity not guaranteed (demo mode)",
			slog.String("cell", c.ID()),
			slog.Int("consistency_level", int(c.ConsistencyLevel())))
	}
	return nil
}

// initSlices constructs all 9 slice services and handlers.
// Extracted from Init to reduce cognitive complexity.
func (c *AccessCore) initSlices() error {
	// session-login must be constructed before identity-manage because
	// ChangePassword injects loginSvc as the TokenIssuer.
	var loginOpts []sessionlogin.Option
	if c.outboxWriter != nil {
		loginOpts = append(loginOpts, sessionlogin.WithOutboxWriter(c.outboxWriter))
	}
	if c.txRunner != nil {
		loginOpts = append(loginOpts, sessionlogin.WithTxManager(c.txRunner))
	}
	loginSvc := sessionlogin.NewService(c.userRepo, c.sessionRepo, c.roleRepo, c.publisher, c.jwtIssuer, c.logger, loginOpts...)
	c.loginHandler = sessionlogin.NewHandler(loginSvc)
	c.AddSlice(cell.NewBaseSlice("session-login", "access-core", cell.L2))

	// identity-manage: inject loginSvc as TokenIssuer for ChangePassword.
	var identityOpts []identitymanage.Option
	if c.outboxWriter != nil {
		identityOpts = append(identityOpts, identitymanage.WithOutboxWriter(c.outboxWriter))
	}
	if c.txRunner != nil {
		identityOpts = append(identityOpts, identitymanage.WithTxManager(c.txRunner))
	}
	identityOpts = append(identityOpts, identitymanage.WithTokenIssuer(loginSvc))
	identitySvc, err := identitymanage.NewService(c.userRepo, c.sessionRepo, c.publisher, c.logger, identityOpts...)
	if err != nil {
		return err
	}
	c.identityHandler = identitymanage.NewHandler(identitySvc)
	c.AddSlice(cell.NewBaseSlice("identity-manage", "access-core", cell.L1))

	// session-validate (before session-refresh: provides session-aware verifier)
	c.validateSvc = sessionvalidate.NewService(c.jwtVerifier, c.sessionRepo, c.logger)
	c.AddSlice(cell.NewBaseSlice("session-validate", "access-core", cell.L0))

	// session-refresh uses jwtVerifier directly (not validateSvc) because
	// validateSvc hard-requires token_use=access and would reject every
	// refresh token. sessionrefresh still enforces session revocation via
	// sessionRepo + Session.IsRevoked checks after JWT verification.
	// WithUserRepository is injected so Refresh can read PasswordResetRequired
	// from the current user state (e.g. after ChangePassword clears the flag).
	refreshSvc := sessionrefresh.NewService(c.sessionRepo, c.roleRepo, c.userRepo, c.jwtIssuer, c.jwtVerifier, c.logger)
	c.refreshHandler = sessionrefresh.NewHandler(refreshSvc)
	c.AddSlice(cell.NewBaseSlice("session-refresh", "access-core", cell.L1))

	// session-logout
	var logoutOpts []sessionlogout.Option
	if c.outboxWriter != nil {
		logoutOpts = append(logoutOpts, sessionlogout.WithOutboxWriter(c.outboxWriter))
	}
	if c.txRunner != nil {
		logoutOpts = append(logoutOpts, sessionlogout.WithTxManager(c.txRunner))
	}
	logoutSvc := sessionlogout.NewService(c.sessionRepo, c.publisher, c.logger, logoutOpts...)
	c.logoutHandler = sessionlogout.NewHandler(logoutSvc)
	c.AddSlice(cell.NewBaseSlice("session-logout", "access-core", cell.L2))

	// authorization-decide
	c.authzSvc = authorizationdecide.NewService(c.roleRepo, c.logger)
	c.AddSlice(cell.NewBaseSlice("authorization-decide", "access-core", cell.L0))

	// rbac-check
	rbacSvc := rbaccheck.NewService(c.roleRepo, c.logger)
	c.rbacHandler = rbaccheck.NewHandler(rbacSvc)
	c.AddSlice(cell.NewBaseSlice("rbac-check", "access-core", cell.L0))

	// rbac-assign — durable mode (outboxWriter + txRunner) upgrades to L2 OutboxFact;
	// demo mode (both nil) stays at L0 (in-memory repos, synchronous dual-write).
	c.initRbacAssign()

	// rbac-session-sync consumer: handles role-change events and invalidates sessions.
	c.rbacSessionConsumer = sessionlogout.NewConsumer(c.sessionRepo, c.logger)

	// config-receive: subscribes to config.changed events from config-core
	c.configReceiveSvc = configreceive.NewService(c.logger)
	c.AddSlice(cell.NewBaseSlice("config-receive", "access-core", cell.L3))
	return nil
}

// initRbacAssign constructs the rbac-assign slice. Extracted to keep initSlices
// within cognitive complexity bounds.
func (c *AccessCore) initRbacAssign() {
	var rbacOpts []rbacassign.Option
	if c.outboxWriter != nil {
		rbacOpts = append(rbacOpts, rbacassign.WithOutboxWriter(c.outboxWriter))
	}
	if c.txRunner != nil {
		rbacOpts = append(rbacOpts, rbacassign.WithTxManager(c.txRunner))
	}
	rbacAssignSvc := rbacassign.NewService(c.roleRepo, c.sessionRepo, c.logger, rbacOpts...)
	c.rbacAssignHandler = rbacassign.NewHandler(rbacAssignSvc)
	rbacAssignLevel := cell.L0
	if c.outboxWriter != nil && c.txRunner != nil {
		rbacAssignLevel = cell.L2
	}
	c.AddSlice(cell.NewBaseSlice("rbacassign", "access-core", rbacAssignLevel))
}

// Init constructs all 9 slices.
func (c *AccessCore) Init(ctx context.Context, deps cell.Dependencies) error {
	if err := c.BaseCell.Init(ctx, deps); err != nil {
		return err
	}
	if err := c.initValidate(deps); err != nil {
		return err
	}
	// Initial admin bootstrap (scheme H): run before constructing slices so
	// that the admin user is available for login immediately after Init returns.
	if err := c.runInitialAdminBootstrap(ctx); err != nil {
		return err
	}
	if err := c.initSlices(); err != nil {
		return err
	}
	return nil
}

// RegisterRoutes registers HTTP routes for access-core.
func (c *AccessCore) RegisterRoutes(mux cell.RouteMux) {
	mux.Route("/api/v1/access", func(sub cell.RouteMux) {
		// Identity management: /api/v1/access/users
		sub.Route("/users", c.identityHandler.RegisterRoutes)

		// Session endpoints: /api/v1/access/sessions
		sub.Route("/sessions", func(s cell.RouteMux) {
			s.Handle("POST /login", http.HandlerFunc(c.loginHandler.HandleLogin))
			s.Handle("POST /refresh", http.HandlerFunc(c.refreshHandler.HandleRefresh))
			s.Handle("DELETE /{id}", http.HandlerFunc(c.logoutHandler.HandleLogout))
		})

		// RBAC queries: /api/v1/access/roles
		sub.Route("/roles", c.rbacHandler.RegisterRoutes)
	})

	// Internal admin endpoints: /internal/v1/access/roles
	mux.Route("/internal/v1/access", func(sub cell.RouteMux) {
		sub.Route("/roles", c.rbacAssignHandler.RegisterRoutes)
	})
}

// RegisterSubscriptions declares event subscriptions for access-core.
// The Router manages goroutine lifecycle and setup-error detection.
func (c *AccessCore) RegisterSubscriptions(r cell.EventRouter) error {
	// config-receive: config.changed events from config-core.
	handler := outbox.WrapLegacyHandler(c.configReceiveSvc.HandleEvent)
	r.AddHandler(configreceive.TopicConfigChanged, handler, "access-core")

	// rbac-session-sync: invalidate sessions on role assignment or revocation.
	// Both topics share the same handler and consumer group — HandleRoleChanged is topic-agnostic.
	roleHandler := outbox.WrapLegacyHandler(c.rbacSessionConsumer.HandleRoleChanged)
	r.AddHandler(dto.TopicRoleAssigned, roleHandler, "access-core-rbac-session-sync")
	r.AddHandler(dto.TopicRoleRevoked, roleHandler, "access-core-rbac-session-sync")
	return nil
}
