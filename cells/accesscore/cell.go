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
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
)

const (
	// defaultAccessCoreRefreshReuseInterval is the token reuse window for the
	// in-memory refresh policy (demo/testing only).
	defaultAccessCoreRefreshReuseInterval = 2 * time.Second
	// defaultAccessCoreRefreshMaxAge is the maximum lifetime of a refresh token
	// for the in-memory refresh policy (demo/testing only).
	defaultAccessCoreRefreshMaxAge = 7 * 24 * time.Hour
)

// defaultRefreshPolicy is used only by WithInMemoryDefaults for demo/testing.
// Durable mode must inject an explicit store via WithRefreshStore.
var defaultRefreshPolicy = refresh.Policy{
	ReuseInterval: defaultAccessCoreRefreshReuseInterval,
	MaxAge:        defaultAccessCoreRefreshMaxAge,
}

// Compile-time interface check lives in cell_gen.go (DO NOT EDIT).

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

// WithMetricsProvider sets the metrics provider used by the DirectEmitter and
// refresh-token GC worker.
func WithMetricsProvider(p metrics.Provider) Option {
	return func(c *AccessCore) { c.metricsProvider = p }
}

// WithConfigEventCollector injects config-event consumer process metrics.
func WithConfigEventCollector(collector obmetrics.ConfigEventCollector) Option {
	return func(c *AccessCore) { c.configEventCollector = collector }
}

// WithClock sets the time source for this Cell. Required — Init() panics via
// clock.MustHaveClock if not set. Composition root passes clock.Real(); tests
// inject a deterministic clock to control time-sensitive logic.
func WithClock(clk clock.Clock) Option {
	return func(c *AccessCore) { c.clk = clk }
}

// WithInMemoryDefaults configures in-memory repositories for development
// and testing. Not suitable for production use.
// sessionRepo and refreshStore construction are deferred to Init() so that
// c.clk is available.
func WithInMemoryDefaults() Option {
	return func(c *AccessCore) {
		c.userRepo = mem.NewUserRepository()
		// sessionRepo construction is deferred to Init() so that c.clk is
		// available for mem.NewSessionRepository.
		c.roleRepo = mem.NewRoleRepository()
		c.useInMemoryDefaults = true
	}
}

// WithInitialAdminBootstrap enables first-run admin bootstrap (scheme H).
// The lifecycle hook is registered via reg.Lifecycle in Init so the bootstrap
// phase wires OnStart/OnStop — no composition-root plumbing required.
//
// ref: docs/architecture/202604181900-adr-auth-setup-first-run.md (scheme H)
func WithInitialAdminBootstrap(opts ...initialadmin.LifecycleOption) Option {
	return func(c *AccessCore) { c.initialAdmin = initialadmin.NewLifecycle(opts...) }
}

// WithConfigGetter injects the ConfigGetter used by the configreceive slice to
// fetch the current config entry value from configcore after an upsert event
// (contract: http.config.internal.get.v1). When not set the slice operates in
// log-only mode — no cross-cell HTTP call is made.
//
// Tests and composition roots inject an implementation directly. Concrete
// factories live in cell-owned adapter subpackages so the root Cell API stays
// port-oriented.
func WithConfigGetter(c ports.ConfigGetter) Option {
	return func(ac *AccessCore) { ac.configGetter = c }
}

// AccessCore is the accesscore Cell implementation.
// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1/access
// +cell:listener:ref=cell.InternalListener,prefix=/internal/v1/access
type AccessCore struct {
	*cell.BaseCell
	clk          clock.Clock
	userRepo     ports.UserRepository
	sessionRepo  ports.SessionRepository
	roleRepo     ports.RoleRepository
	refreshStore refresh.Store

	// useInMemoryDefaults tracks whether WithInMemoryDefaults was applied so
	// Init() can construct the refreshStore (which needs c.clk) after deps are wired.
	useInMemoryDefaults bool

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

	metricsProvider      metrics.Provider
	configEventCollector obmetrics.ConfigEventCollector
	refreshGCEnabled     bool
	refreshGCInterval    time.Duration
	refreshGCRetention   time.Duration
	refreshGCCollector   refresh.GCCollector
	refreshGC            *refresh.GCWorker

	// initialAdmin wires first-run admin bootstrap via reg.Lifecycle(...);
	// nil means the feature is disabled.
	initialAdmin *initialadmin.Lifecycle

	// configGetter is used by the configreceive slice to fetch config entry
	// values from configcore after an upsert event. nil = log-only mode.
	configGetter ports.ConfigGetter

	// Slice handlers.
	// +slice:route:slice=identitymanage,subPath=/users
	identityHandler *identitymanage.Handler

	// +slice:route:slice=sessionlogin,subPath=/sessions
	loginHandler *sessionlogin.Handler

	// +slice:route:slice=sessionrefresh,subPath=/sessions
	refreshHandler *sessionrefresh.Handler

	// +slice:route:slice=sessionlogout,subPath=/sessions
	logoutHandler *sessionlogout.Handler

	// +slice:route:slice=setup,subPath=/setup
	setupHandler *setup.Handler

	// Services exposed for composition (e.g. TokenVerifier, Authorizer).
	validateSvc *sessionvalidate.Service
	authzSvc    *authorizationdecide.Service

	// +slice:route:slice=rbaccheck,subPath=/roles
	rbacHandler     *rbaccheck.Handler
	rbacRunMode     query.RunMode
	rbacEmitterMode bool

	// +slice:route:slice=rbacassign,listener=cell.InternalListener,subPath=/roles
	rbacAssignHandler *rbacassign.Handler

	// +slice:subscribe:slice=configreceive,topic=event.config.entry-upserted.v1,handler=HandleEntryUpserted,group=accesscore
	// +slice:subscribe:slice=configreceive,topic=event.config.entry-deleted.v1,handler=HandleEntryDeleted,group=accesscore
	configReceiveSvc *configreceive.Service

	// +slice:subscribe:slice=sessionlogout,topic=event.role.assigned.v1,handler=HandleRoleChanged,group=accesscore-rbac-session-sync
	// +slice:subscribe:slice=sessionlogout,topic=event.role.revoked.v1,handler=HandleRoleChanged,group=accesscore-rbac-session-sync
	rbacSessionConsumer *sessionlogout.Consumer
}

// NewAccessCore creates a new AccessCore Cell.
func NewAccessCore(opts ...Option) *AccessCore {
	c := &AccessCore{
		BaseCell: cell.MustNewBaseCell(loadCellMetadata()),
		logger:   slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}
