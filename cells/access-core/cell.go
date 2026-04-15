// Package accesscore implements the access-core Cell: identity management,
// session lifecycle (login/refresh/logout/validate), RBAC authorization,
// and role queries.
package accesscore

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/cells/access-core/internal/mem"
	"github.com/ghbvf/gocell/cells/access-core/internal/ports"
	"github.com/ghbvf/gocell/cells/access-core/slices/authorizationdecide"
	"github.com/ghbvf/gocell/cells/access-core/slices/configreceive"
	"github.com/ghbvf/gocell/cells/access-core/slices/identitymanage"
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
)

// Compile-time interface checks.
var (
	_ cell.Cell           = (*AccessCore)(nil)
	_ cell.HTTPRegistrar  = (*AccessCore)(nil)
	_ cell.EventRegistrar = (*AccessCore)(nil)
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

// WithInMemoryDefaults configures in-memory repositories for development
// and testing. Not suitable for production use.
func WithInMemoryDefaults() Option {
	return func(c *AccessCore) {
		c.userRepo = mem.NewUserRepository()
		c.sessionRepo = mem.NewSessionRepository()
		c.roleRepo = mem.NewRoleRepository()
	}
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

	// Slice handlers.
	identityHandler *identitymanage.Handler
	loginHandler    *sessionlogin.Handler
	refreshHandler  *sessionrefresh.Handler
	logoutHandler   *sessionlogout.Handler

	// Services exposed for composition (e.g. TokenVerifier, Authorizer).
	validateSvc      *sessionvalidate.Service
	authzSvc         *authorizationdecide.Service
	rbacHandler      *rbaccheck.Handler
	configReceiveSvc *configreceive.Service
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

// TokenVerifier returns the session-validate service (implements auth.TokenVerifier).
func (c *AccessCore) TokenVerifier() auth.TokenVerifier {
	if c.validateSvc == nil {
		return nil
	}
	return c.validateSvc
}

// Authorizer returns the authorization-decide service (implements auth.Authorizer).
func (c *AccessCore) Authorizer() auth.Authorizer {
	return c.authzSvc
}

// Init constructs all 8 slices.
func (c *AccessCore) Init(ctx context.Context, deps cell.Dependencies) error {
	if err := c.BaseCell.Init(ctx, deps); err != nil {
		return err
	}

	// Fail-fast: outboxWriter and txRunner must be both present or both absent (XOR constraint).
	// Both present = durable mode (L2 atomicity). Both absent = demo/in-memory mode.
	if (c.outboxWriter == nil) != (c.txRunner == nil) {
		return errcode.New(errcode.ErrCellMissingOutbox,
			"access-core durable mode requires both outboxWriter and txRunner")
	}

	// Demo mode: both nil → require publisher for degraded event delivery.
	if c.outboxWriter == nil && c.txRunner == nil {
		if c.publisher == nil {
			return errcode.New(errcode.ErrCellMissingOutbox,
				"access-core requires publisher or outbox writer; use WithPublisher(outbox.DiscardPublisher{}) for demo mode")
		}
		if c.ConsistencyLevel() >= cell.L2 {
			c.logger.Warn("access-core: running without outboxWriter+txRunner, L2 transactional atomicity not guaranteed (demo mode)")
		}
	}

	// Fail-fast: RS256 key pair required.
	if c.jwtIssuer == nil || c.jwtVerifier == nil {
		return errcode.New(errcode.ErrAuthKeyInvalid,
			"RS256 key pair required: use WithJWTIssuer and WithJWTVerifier")
	}

	// identity-manage
	var identityOpts []identitymanage.Option
	if c.outboxWriter != nil {
		identityOpts = append(identityOpts, identitymanage.WithOutboxWriter(c.outboxWriter))
	}
	if c.txRunner != nil {
		identityOpts = append(identityOpts, identitymanage.WithTxManager(c.txRunner))
	}
	identitySvc := identitymanage.NewService(c.userRepo, c.sessionRepo, c.publisher, c.logger, identityOpts...)
	c.identityHandler = identitymanage.NewHandler(identitySvc)
	c.AddSlice(cell.NewBaseSlice("identity-manage", "access-core", cell.L1))

	// session-login
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

	// session-validate (before session-refresh: provides session-aware verifier)
	c.validateSvc = sessionvalidate.NewService(c.jwtVerifier, c.sessionRepo, c.logger)
	c.AddSlice(cell.NewBaseSlice("session-validate", "access-core", cell.L0))

	// session-refresh — uses session-aware verifier (validateSvc) so that
	// revoked/expired sessions are caught at the JWT verification step,
	// not just at the DB refresh-token lookup.
	refreshSvc := sessionrefresh.NewService(c.sessionRepo, c.roleRepo, c.jwtIssuer, c.validateSvc, c.logger)
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

	// config-receive: subscribes to config.changed events from config-core
	c.configReceiveSvc = configreceive.NewService(c.logger)
	c.AddSlice(cell.NewBaseSlice("config-receive", "access-core", cell.L3))

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
}

// RegisterSubscriptions declares event subscriptions for access-core.
// The Router manages goroutine lifecycle and setup-error detection.
func (c *AccessCore) RegisterSubscriptions(r cell.EventRouter) error {
	handler := outbox.WrapLegacyHandler(c.configReceiveSvc.HandleEvent)
	r.AddHandler(configreceive.TopicConfigChanged, handler, "access-core")
	return nil
}
