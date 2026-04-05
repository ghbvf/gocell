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
	"github.com/ghbvf/gocell/cells/access-core/slices/identitymanage"
	"github.com/ghbvf/gocell/cells/access-core/slices/rbaccheck"
	"github.com/ghbvf/gocell/cells/access-core/slices/sessionlogin"
	"github.com/ghbvf/gocell/cells/access-core/slices/sessionlogout"
	"github.com/ghbvf/gocell/cells/access-core/slices/sessionrefresh"
	"github.com/ghbvf/gocell/cells/access-core/slices/sessionvalidate"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
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

// WithSigningKey sets the JWT signing key.
func WithSigningKey(key []byte) Option {
	return func(c *AccessCore) { c.signingKey = key }
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
	userRepo    ports.UserRepository
	sessionRepo ports.SessionRepository
	roleRepo    ports.RoleRepository
	publisher   outbox.Publisher
	logger      *slog.Logger
	signingKey  []byte

	// Slice handlers.
	identityHandler *identitymanage.Handler
	loginHandler    *sessionlogin.Handler
	refreshHandler  *sessionrefresh.Handler
	logoutHandler   *sessionlogout.Handler

	// Services exposed for composition (e.g. TokenVerifier, Authorizer).
	validateSvc *sessionvalidate.Service
	authzSvc    *authorizationdecide.Service
	rbacHandler *rbaccheck.Handler
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
	return c.validateSvc
}

// Authorizer returns the authorization-decide service (implements auth.Authorizer).
func (c *AccessCore) Authorizer() auth.Authorizer {
	return c.authzSvc
}

// Init constructs all 7 slices.
func (c *AccessCore) Init(ctx context.Context, deps cell.Dependencies) error {
	if err := c.BaseCell.Init(ctx, deps); err != nil {
		return err
	}

	// Resolve signing key from Dependencies.Config if not set via option.
	if len(c.signingKey) == 0 {
		if key, ok := deps.Config["access.signing_key"]; ok {
			if keyStr, ok := key.(string); ok && keyStr != "" {
				c.signingKey = []byte(keyStr)
			}
		}
	}
	if len(c.signingKey) == 0 {
		return errcode.New("ERR_AUTH_MISSING_KEY", "JWT signing key is required")
	}
	if len(c.signingKey) < 32 {
		return errcode.New("ERR_AUTH_MISSING_KEY", "JWT signing key must be at least 32 bytes")
	}

	// identity-manage
	identitySvc := identitymanage.NewService(c.userRepo, c.sessionRepo, c.publisher, c.logger)
	c.identityHandler = identitymanage.NewHandler(identitySvc)
	c.AddSlice(cell.NewBaseSlice("identity-manage", "access-core", cell.L1))

	// session-login
	loginSvc := sessionlogin.NewService(c.userRepo, c.sessionRepo, c.roleRepo, c.publisher, c.signingKey, c.logger)
	c.loginHandler = sessionlogin.NewHandler(loginSvc)
	c.AddSlice(cell.NewBaseSlice("session-login", "access-core", cell.L2))

	// session-refresh
	refreshSvc := sessionrefresh.NewService(c.sessionRepo, c.roleRepo, c.signingKey, c.logger)
	c.refreshHandler = sessionrefresh.NewHandler(refreshSvc)
	c.AddSlice(cell.NewBaseSlice("session-refresh", "access-core", cell.L1))

	// session-logout
	logoutSvc := sessionlogout.NewService(c.sessionRepo, c.publisher, c.logger)
	c.logoutHandler = sessionlogout.NewHandler(logoutSvc)
	c.AddSlice(cell.NewBaseSlice("session-logout", "access-core", cell.L2))

	// session-validate
	c.validateSvc = sessionvalidate.NewService(c.signingKey, c.sessionRepo, c.logger)
	c.AddSlice(cell.NewBaseSlice("session-validate", "access-core", cell.L0))

	// authorization-decide
	c.authzSvc = authorizationdecide.NewService(c.roleRepo, c.logger)
	c.AddSlice(cell.NewBaseSlice("authorization-decide", "access-core", cell.L0))

	// rbac-check
	rbacSvc := rbaccheck.NewService(c.roleRepo, c.logger)
	c.rbacHandler = rbaccheck.NewHandler(rbacSvc)
	c.AddSlice(cell.NewBaseSlice("rbac-check", "access-core", cell.L0))

	return nil
}

// RegisterRoutes registers HTTP routes for access-core.
func (c *AccessCore) RegisterRoutes(mux cell.RouteMux) {
	mux.Route("/api/v1/access", func(sub cell.RouteMux) {
		// Identity management: /api/v1/access/users
		sub.Mount("/users", c.identityHandler.Routes())

		// Session endpoints: /api/v1/access/sessions
		sub.Route("/sessions", func(s cell.RouteMux) {
			s.Handle("POST /login", http.HandlerFunc(c.loginHandler.HandleLogin))
			s.Handle("POST /refresh", http.HandlerFunc(c.refreshHandler.HandleRefresh))
			s.Handle("DELETE /{id}", http.HandlerFunc(c.logoutHandler.HandleLogout))
		})

		// RBAC queries: /api/v1/access/roles
		sub.Mount("/roles", c.rbacHandler.Routes())
	})
}

// RegisterSubscriptions is a no-op for access-core in Phase 2.
// Future: subscribe to cross-cell events if needed.
func (c *AccessCore) RegisterSubscriptions(_ outbox.Subscriber) {
	// No subscriptions in Phase 2.
}
