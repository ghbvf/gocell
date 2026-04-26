// Package accesscore implements the accesscore Cell: identity management,
// session lifecycle (login/refresh/logout/validate), RBAC authorization,
// and role queries.
package accesscore

import (
	"log/slog"
	"time"

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
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	refreshmem "github.com/ghbvf/gocell/runtime/auth/refresh/memstore"
)

// defaultRefreshPolicy is used only by WithInMemoryDefaults for demo/testing.
// Durable mode must inject an explicit store via WithRefreshStore.
var defaultRefreshPolicy = refresh.Policy{
	ReuseInterval: 2 * time.Second,
	MaxAge:        7 * 24 * time.Hour,
}

// realClock is a minimal refresh.Clock implementation backed by time.Now.
type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }

// Compile-time interface checks.
var (
	_ cell.Cell                  = (*AccessCore)(nil)
	_ cell.RouteGroupContributor = (*AccessCore)(nil)
	_ cell.HealthContributor     = (*AccessCore)(nil)
	_ cell.EventRegistrar        = (*AccessCore)(nil)
	_ cell.LifecycleContributor  = (*AccessCore)(nil)
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

// WithEmitter injects a pre-composed outbox.Emitter directly into the Cell.
// Preferred path for tests and for composition roots that have already built
// an Emitter (e.g. outbox.NewNoopEmitter(), a custom wrapper, or a fake that
// records outbox entries for assertions).
//
// Mutually exclusive with WithOutboxDeps — setting both causes Init() to
// fail fast with ErrCellInvalidConfig. Durability for L2 slice upgrades is
// derived from outbox.ReportDurable(emitter); Emitter implementations that
// do not expose DurabilityReporter are treated as non-durable.
//
// ref: kubernetes/client-go rest.RESTClientFor — factory composes the typed
// client; resulting struct does not retain raw config fields.
func WithEmitter(e outbox.Emitter) Option {
	return func(c *AccessCore) { c.emitter = e }
}

// WithOutboxDeps wires raw outbox dependencies (Publisher + Writer) into the
// Cell. The framework composes them into an outbox.Emitter at Init() time via
// cell.ResolveEmitter, applying the cell's durability-mode policy.
//
// Accumulative: a nil argument leaves the previously-set value in place, so
// `WithOutboxDeps(pub, nil)` and `WithOutboxDeps(nil, writer)` may be called
// separately to wire publisher and writer independently. The pairing rules in
// ResolveEmitter still apply (demo mode allows publisher-only; durable mode
// requires real writer + txRunner).
//
// Does NOT clear previously-set deps: `WithOutboxDeps(nil, nil)` is a no-op,
// not a reset. To switch between direct-injection (WithEmitter) and composed
// (WithOutboxDeps) paths, construct a fresh Cell instead of trying to toggle.
//
// Mutually exclusive with WithEmitter — Init() fails fast if both are set.
func WithOutboxDeps(pub outbox.Publisher, writer outbox.Writer) Option {
	return func(c *AccessCore) {
		if pub != nil {
			c.pendingOutboxPub = pub
		}
		if writer != nil {
			c.pendingOutboxWriter = writer
		}
	}
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

// WithCursorCodec sets the cursor codec for pagination. Required in durable mode.
func WithCursorCodec(codec *query.CursorCodec) Option {
	return func(c *AccessCore) { c.cursorCodec = codec }
}

// WithTxManager sets the TxRunner for transactional guarantees (L2 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(c *AccessCore) { c.txRunner = tx }
}

// WithRefreshStore injects the refresh.Store used for opaque refresh token
// Issue/Rotate/Revoke. Required in production (durable) mode — demo mode
// falls back to an in-memory store via WithInMemoryDefaults.
func WithRefreshStore(store refresh.Store) Option {
	return func(c *AccessCore) { c.refreshStore = store }
}

// WithRefreshGC enables the refresh-token GC lifecycle worker.
func WithRefreshGC(interval, retention time.Duration) Option {
	return func(c *AccessCore) {
		c.refreshGCEnabled = true
		c.refreshGCInterval = interval
		c.refreshGCRetention = retention
	}
}

// WithRefreshMetricsProvider sets the metrics provider used by the refresh-token
// GC worker. This is distinct from WithMetricsProvider (on auditcore/configcore)
// which wires a metrics.Provider into the outbox DirectEmitter: refresh GC uses
// the provider for GC-specific counters only and does not affect event publishing.
func WithRefreshMetricsProvider(p metrics.Provider) Option {
	return func(c *AccessCore) { c.metricsProvider = p }
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
		c.refreshStore = refreshmem.New(defaultRefreshPolicy, realClock{}, nil)
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

// WithConfigClient injects the ConfigClient used by the configreceive slice to
// fetch the current config entry value from configcore after an upsert event
// (contract: http.config.internal.get.v1). When not set the slice operates in
// log-only mode — no cross-cell HTTP call is made.
func WithConfigClient(c ports.ConfigClient) Option {
	return func(ac *AccessCore) { ac.configClient = c }
}

// AccessCore is the accesscore Cell implementation.
type AccessCore struct {
	*cell.BaseCell
	userRepo     ports.UserRepository
	sessionRepo  ports.SessionRepository
	roleRepo     ports.RoleRepository
	refreshStore refresh.Store

	// Outbox wiring. Two mutually exclusive paths populate `emitter`:
	//   (a) WithEmitter(e)          — `emitter` is set pre-Init.
	//   (b) WithOutboxDeps(pub, w)  — pendingOutboxPub/Writer are set and
	//       Init() composes an Emitter via cell.ResolveEmitter.
	// After Init, pendingOutboxPub/Writer are cleared; only `emitter` is live.
	// These fields are private — no exported Option is allowed to take raw
	// outbox.Publisher/Writer arguments (enforced by archtest OUTBOX-CELL-01).
	emitter             outbox.Emitter
	pendingOutboxPub    outbox.Publisher
	pendingOutboxWriter outbox.Writer

	txRunner    persistence.TxRunner
	logger      *slog.Logger
	jwtIssuer   *auth.JWTIssuer
	jwtVerifier *auth.JWTVerifier
	cursorCodec *query.CursorCodec

	metricsProvider    metrics.Provider
	refreshGCEnabled   bool
	refreshGCInterval  time.Duration
	refreshGCRetention time.Duration
	refreshGCCollector refresh.GCCollector
	refreshGC          *refresh.GCWorker

	// initialAdmin wires first-run admin bootstrap via LifecycleContributor;
	// nil means the feature is disabled.
	initialAdmin *initialadmin.Lifecycle

	// configClient is used by the configreceive slice to fetch config entry
	// values from configcore after an upsert event. nil = log-only mode.
	configClient ports.ConfigClient

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
