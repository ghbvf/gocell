// Package accesscore implements the accesscore Cell: identity management,
// session lifecycle (login/refresh/logout/validate), RBAC authorization,
// and role queries.
package accesscore

import (
	"log/slog"
	"net/http"
	"time"

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
	"github.com/ghbvf/gocell/pkg/validation"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/auth/refresh"
	"github.com/ghbvf/gocell/runtime/auth/session"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

// PasswordVersionField is the DB column name used as the CAS version field
// for ChangePassword optimistic-concurrency control. Composition root uses
// this constant when wiring cas.Protocol for the user table:
//
//	cas.MustNewProtocol(cas.WithVersionField(accesscore.PasswordVersionField))
const PasswordVersionField = "password_version"

// Compile-time interface check lives in cell_gen.go (DO NOT EDIT).

// Option configures an AccessCore Cell.
type Option func(*AccessCore)

// WithUserRepository sets the UserRepository.
func WithUserRepository(r ports.UserRepository) Option {
	return func(c *AccessCore) { c.userRepo = r }
}

// WithSessionStore injects the session.Store used for session lifecycle
// (create / get / revoke / revokeForSubject). Required — Init() fails with
// ErrCellInvalidConfig when nil.
//
// Strong-dependency wiring option: both bare-nil and typed-nil session.Store
// are rejected at phase0 (via sessionStoreNil sentinel). Pass
// session.NewMemStore or adapters/postgres.NewSessionStore from the
// composition root.
//
// ref: runtime-api.md §Option 范式分层 — wiring option, nil rejected at phase0.
func WithSessionStore(s session.Store) Option {
	return func(c *AccessCore) {
		if validation.IsNilInterface(s) {
			c.sessionStoreNil = true
			return
		}
		c.sessionStore = s
	}
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

// WithOutboxDeps 注入 sealed CellPublisher 和 CellWriter，由 composition root
// 通过 outbox.WrapPublisherForCell / outbox.WrapWriterForCell 包装得到。
// 框架在 Init() 时通过 cell.ResolveEmitter 将二者组合为 outbox.Emitter，
// 并应用 cell 的 durability-mode 策略。
//
// 详见 ADR 202605101900-adr-cell-raw-infra-sealed-marker §D1。
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
func WithOutboxDeps(pub outbox.CellPublisher, writer outbox.CellWriter) Option {
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

// WithTxManager sets the CellTxManager for transactional guarantees (L2
// atomicity). Composition roots construct via persistence.WrapForCell.
func WithTxManager(tx persistence.CellTxManager) Option {
	return func(c *AccessCore) { c.txRunner = tx }
}

// WithRefreshStore injects the refresh.Store used for opaque refresh token
// Issue/Rotate/Revoke. Required — Init() fails with ErrCellMissingTokenIssuer
// when nil. Composition root passes the mem or PG store.
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

// WithSetupLock injects a cross-process advisory lock for the admin-provisioning
// path (multi-pod PG deployments). When set, CreateAdmin acquires the lock at
// the start of the RunInTx body before calling adminprovision.Ensure — the lock,
// user write, and outbox emit share one transaction. Nil is a no-op (mem mode
// keeps the intra-process sync.Mutex). Closes backlog ADMINPROVISION-DIST-LOCK-01.
func WithSetupLock(lock ports.SetupLock) Option {
	return func(c *AccessCore) {
		if lock != nil {
			c.setupLock = lock
		}
	}
}

// WithCASProtocol injects the CAS Protocol used by the ChangePassword path
// (S6 CHANGEPASSWORD-CONCURRENT-SEMANTICS-01). The Protocol declares which DB
// column carries the monotonic version counter and which conflict policy to
// apply on mismatch.
//
// REQUIRED: initValidate() rejects nil with ErrCellInvalidConfig so that the
// cell will not start without a properly-configured CAS primitive.
// Composition root constructs the Protocol via cas.MustNewProtocol and passes
// it here; cells must not construct it directly (CAS-PROTOCOL-COMPOSITION-ROOT-01
// archtest enforces this).
//
// Both bare-nil and typed-nil *cas.Protocol are rejected at phase0.
func WithCASProtocol(p *cas.Protocol) Option {
	return func(c *AccessCore) {
		if p != nil {
			c.casProtocol = p
		}
	}
}

// WithBootstrapAuth injects the per-route replacement authentication
// middleware for the admin setup endpoint (POST /api/v1/access/setup/admin).
//
// The composition root passes runtime/auth.NewBootstrapMiddleware so that the
// endpoint is gated by Basic Auth credentials from GOCELL_BOOTSTRAP_ADMIN_*
// env vars (D5: env creds authenticate the operator; request body defines the
// admin identity). This applies in both bootstrap and interactive modes — the
// operator Basic Auth credential (ADR §D2) makes the protection a
// permanent requirement, not an interactive-only feature.
//
// REQUIRED: Init() returns ErrCellInvalidConfig when nil. The closed contract
// established by codegen + runtime/auth.Route.BootstrapAuth requires this to
// be wired by the composition root before slice initialisation.
func WithBootstrapAuth(mw func(http.Handler) http.Handler) Option {
	return func(c *AccessCore) { c.bootstrapAuth = mw }
}

// AccessCore is the accesscore Cell implementation.
// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1/access
// +cell:listener:ref=cell.InternalListener,prefix=/internal/v1/access
type AccessCore struct {
	*cell.BaseCell
	clk          clock.Clock
	userRepo     ports.UserRepository
	sessionStore session.Store
	roleRepo     ports.RoleRepository
	refreshStore refresh.Store

	// sessionStoreNil is set by WithSessionStore when a nil session.Store is
	// passed. Phase0 validation rejects the cell when this sentinel is true
	// so the error is associated with the option name rather than surfacing as
	// a cryptic nil-pointer dereference inside initSlices.
	sessionStoreNil bool

	// Outbox wiring. Two mutually exclusive paths populate `emitter`:
	//   (a) WithEmitter(e)          — `emitter` is set pre-Init.
	//   (b) WithOutboxDeps(pub, w)  — pendingOutboxPub/Writer are set and
	//       Init() composes an Emitter via cell.ResolveEmitter.
	// After Init, pendingOutboxPub/Writer are cleared; only `emitter` is live.
	// Sealed marker types prevent any cell.go public Option from accepting
	// raw outbox.Publisher / outbox.Writer at compile time (ADR
	// cell-raw-infra-sealed-marker §D1).
	emitter             outbox.Emitter
	pendingOutboxPub    outbox.CellPublisher
	pendingOutboxWriter outbox.CellWriter

	txRunner    persistence.CellTxManager
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

	// configGetter is used by the configreceive slice to fetch config entry
	// values from configcore after an upsert event. nil = log-only mode.
	configGetter ports.ConfigGetter

	// bootstrapAuth is the per-route replacement authentication middleware
	// for the POST /setup/admin endpoint. The composition root injects
	// runtime/auth.NewBootstrapMiddleware here; Init() rejects a nil value
	// because the closed contract (auth.Route.BootstrapAuth) requires it.
	// Persistent operator authenticator on the single setup-driven admin path (ADR §D2).
	bootstrapAuth func(http.Handler) http.Handler

	// setupLock is an optional cross-process advisory lock injected by the PG
	// composition root (accesscore/postgres.NewSetupLock). Nil in mem mode — the
	// intra-process sync.Mutex in adminprovision.Provisioner is sufficient.
	// Closes backlog ADMINPROVISION-DIST-LOCK-01.
	setupLock ports.SetupLock

	// casProtocol is the CAS primitive for the ChangePassword path (S6).
	// Required — initValidate() rejects nil. Composition root injects via
	// WithCASProtocol; CAS-PROTOCOL-COMPOSITION-ROOT-01 archtest enforces that
	// cells never construct Protocol directly.
	casProtocol *cas.Protocol

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
