// Package accesscore implements the accesscore Cell: identity management,
// session lifecycle (login/refresh/logout/validate), RBAC authorization,
// and role queries.
package accesscore

import (
	"log/slog"

	"github.com/ghbvf/gocell/cells/accesscore/initialadmin"
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
	"github.com/ghbvf/gocell/cells/accesscore/slices/setup"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
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
// GOCELL_STATE_DIR, which overrides the platform default state directory.
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
	setupHandler    *setup.Handler

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
