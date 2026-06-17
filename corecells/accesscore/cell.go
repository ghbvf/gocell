// Package accesscore implements the accesscore Cell: identity management,
// session lifecycle (login/refresh/logout/validate), RBAC authorization,
// and role queries.
package accesscore

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/corecells/accesscore/internal/accountlockout"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/credential"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/credentialinvalidate"
	"github.com/ghbvf/gocell/corecells/accesscore/internal/ports"
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
	"github.com/ghbvf/gocell/corecells/accesscore/slices/sessionverifyrpc"
	"github.com/ghbvf/gocell/corecells/accesscore/slices/setup"
	"github.com/ghbvf/gocell/framework/kernel/cell"
	"github.com/ghbvf/gocell/framework/kernel/clock"
	"github.com/ghbvf/gocell/framework/kernel/observability/metrics"
	"github.com/ghbvf/gocell/framework/kernel/outbox"
	"github.com/ghbvf/gocell/framework/kernel/persistence"
	"github.com/ghbvf/gocell/framework/pkg/query"
	"github.com/ghbvf/gocell/framework/pkg/redaction"
	"github.com/ghbvf/gocell/framework/pkg/validation"
	"github.com/ghbvf/gocell/framework/runtime/auth"
	"github.com/ghbvf/gocell/framework/runtime/auth/refresh"
	"github.com/ghbvf/gocell/framework/runtime/auth/session"
	obmetrics "github.com/ghbvf/gocell/framework/runtime/observability/metrics"
	"github.com/ghbvf/gocell/framework/runtime/state/cas"
)

// PasswordVersionField is the DB column name used as the CAS version field
// for ChangePassword optimistic-concurrency control. Composition root uses
// this constant when wiring cas.Protocol for the user table:
//
//	cas.NewProtocol(cas.WithVersionField(accesscore.PasswordVersionField))
const PasswordVersionField = "password_version"

// Compile-time interface check lives in cell_gen.go (DO NOT EDIT).

// Option configures an AccessCore Cell.
type Option func(*AccessCore)

// withUserRepository sets the UserRepository. Unexported — composition roots
// wire via WithMemBundle / WithPGBundle so the UserRepository, RoleRepository,
// SetupLock and TxManager originate from the same backing Store. Hard funnel
// (ACCESSCORE-BUNDLE-FUNNEL-01): no public symbol can wire UserRepository
// independently of its sibling repositories or TxRunner.
func withUserRepository(r ports.UserRepository) Option {
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

// withRoleRepository sets the RoleRepository. Unexported — see
// withUserRepository godoc for the bundle funnel rationale.
func withRoleRepository(r ports.RoleRepository) Option {
	return func(c *AccessCore) { c.roleRepo = r }
}

// withPolicyRepository sets the ABAC PolicyRepository (#1346 PR-8). Unexported —
// see withUserRepository godoc for the bundle funnel rationale; the policy store
// is wired through the same WithMemBundle / WithPGBundle funnel as its siblings.
func withPolicyRepository(r ports.PolicyRepository) Option {
	return func(c *AccessCore) { c.policyRepo = r }
}

// withResourceAttributeProvider sets the ABAC PIP (Policy Information Point)
// for resource attributes (PR-9 #1347). Unexported — wired through the same
// WithMemBundle / WithPGBundle funnel as its siblings so composition roots
// cannot accidentally omit the provider. The mem bundle seeds an empty provider
// (fail-closed); PG bundle also uses the empty mem provider until the
// PG-backed resource_attributes store lands (#1347 follow-up).
func withResourceAttributeProvider(p ports.ResourceAttributeProvider) Option {
	return func(c *AccessCore) { c.resourceAttrs = p }
}

// WithEmitter injects a pre-composed outbox.CellEmitter directly into the Cell.
// Preferred path for tests and for composition roots that have already built
// a CellEmitter (e.g. outbox.DemoCellEmitter(), a recorder via
// outboxtest.Recorder.CellEmitter(), or a wrapped emitter via
// outbox.WrapEmitterForCell).
//
// Mutually exclusive with WithOutboxDeps — setting both causes Init() to
// fail fast with ErrCellInvalidConfig. Durability for L2 slice upgrades is
// derived from the emitter's Durable() method.
//
// ref: kubernetes/client-go rest.RESTClientFor — factory composes the typed
// client; resulting struct does not retain raw config fields.
func WithEmitter(e outbox.CellEmitter) Option {
	return func(c *AccessCore) { c.emitter = e }
}

// WithOutboxDeps 注入 sealed CellPublisher 和 CellWriter，由 composition root
// 通过 outbox.WrapPublisherForCell / outbox.WrapWriterForCell 包装得到。
// 框架在 Init() 时通过 outbox.ResolveEmitter 将二者组合为 outbox.Emitter，
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

// WithPasswordHasher overrides the password hasher threaded to the setup and
// identity-manage services. Defaults to credential.NewProductionHasher() (cost
// 12); tests and integration harnesses pass credential.NewTestHasher(
// bcrypt.MinCost) to avoid the ~1.5s/hash cost. A bare/typed-nil hasher is
// ignored so the production default survives. BCRYPT-COST-FUNNEL-01 rule A2
// keeps NewTestHasher out of production code.
func WithPasswordHasher(h credential.Hasher) Option {
	return func(c *AccessCore) {
		if validation.IsNilInterface(h) {
			return
		}
		c.passwordHasher = h
	}
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

// withTxManager sets the CellTxManager for transactional guarantees (L2
// atomicity). Unexported — bundles always carry the Store-paired TxRunner
// alongside their repositories, so independent caller wiring of TxManager
// is disallowed by design. See withUserRepository godoc.
func withTxManager(tx persistence.CellTxManager) Option {
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

// WithLockoutMetrics injects the auto-lockout observability recorder. The
// composition root constructs auth.NewAccountLockoutMetrics(p) and passes
// the result here. Nil is silently ignored (mem/demo mode falls back to a
// no-op recorder inside accountlockout.NewService).
func WithLockoutMetrics(rec accountlockout.MetricsRecorder) Option {
	return func(c *AccessCore) {
		if rec != nil {
			c.lockoutMetrics = rec
		}
	}
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

// withSetupLock injects the cross-process advisory lock for the
// admin-provisioning path. Unexported — bundles always carry the correct
// SetupLock for their backend (NoopSetupLock for mem, pg_advisory_xact_lock
// for PG). Composition roots cannot accidentally pair the wrong SetupLock
// with a TxRunner from a different store.
//
// Both bare-nil and typed-nil ports.SetupLockAcquirer are rejected at phase0
// (setupLockNil sentinel + initValidate check).
func withSetupLock(lock ports.SetupLockAcquirer) Option {
	return func(c *AccessCore) {
		if validation.IsNilInterface(lock) {
			c.setupLockNil = true
			return
		}
		c.setupLock = lock
	}
}

// WithCASProtocol injects the CAS Protocol used by the ChangePassword path
// (S6 CHANGEPASSWORD-CONCURRENT-SEMANTICS-01). The Protocol declares which DB
// column carries the monotonic version counter and which conflict policy to
// apply on mismatch.
//
// REQUIRED: initValidate() rejects nil with ErrCellInvalidConfig so that the
// cell will not start without a properly-configured CAS primitive.
// Composition root constructs the Protocol via cas.NewProtocol and passes
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
	clk           clock.Clock
	userRepo      ports.UserRepository
	sessionStore  session.Store
	roleRepo      ports.RoleRepository
	policyRepo    ports.PolicyRepository
	resourceAttrs ports.ResourceAttributeProvider
	refreshStore  refresh.Store

	// sessionStoreNil is set by WithSessionStore when a nil session.Store is
	// passed. Phase0 validation rejects the cell when this sentinel is true
	// so the error is associated with the option name rather than surfacing as
	// a cryptic nil-pointer dereference inside initSlices.
	sessionStoreNil bool

	// Outbox wiring. Two mutually exclusive paths populate `emitter`:
	//   (a) WithEmitter(e)          — `emitter` is set pre-Init.
	//   (b) WithOutboxDeps(pub, w)  — pendingOutboxPub/Writer are set and
	//       Init() composes an Emitter via outbox.ResolveEmitter.
	// After Init, pendingOutboxPub/Writer are cleared; only `emitter` is live.
	// Sealed marker types prevent any cell.go public Option from accepting
	// raw outbox.Publisher / outbox.Writer at compile time (ADR
	// cell-raw-infra-sealed-marker §D1).
	emitter             outbox.CellEmitter
	pendingOutboxPub    outbox.CellPublisher
	pendingOutboxWriter outbox.CellWriter

	txRunner    persistence.CellTxManager
	logger      *slog.Logger
	jwtIssuer   *auth.JWTIssuer
	jwtVerifier *auth.JWTVerifier
	cursorCodec *query.CursorCodec

	// invalidator is constructed in initSlices and shared by identity-manage,
	// rbac-assign, and session-refresh to atomically cascade credential revocation.
	invalidator *credentialinvalidate.Invalidator

	metricsProvider      metrics.Provider
	configEventCollector obmetrics.ConfigEventCollector

	// lockoutMetrics is the observability recorder injected by the composition
	// root for ACCESSCORE-ACCOUNT-LOCKOUT-AUTO-LOCK-01. accountlockout.Service
	// calls IncAccountLockout("threshold_locked"|"lazy_unlocked") on transitions.
	// Nil → accountlockout uses its internal no-op recorder (mem/demo mode).
	lockoutMetrics     accountlockout.MetricsRecorder
	refreshGCEnabled   bool
	refreshGCInterval  time.Duration
	refreshGCRetention time.Duration
	refreshGCCollector refresh.GCCollector
	refreshGC          *refresh.GCWorker

	// configGetter is used by the configreceive slice to fetch config entry
	// values from configcore after an upsert event. nil = log-only mode.
	configGetter ports.ConfigGetter

	// bootstrapAuth is the per-route replacement authentication middleware
	// for the POST /setup/admin endpoint. The composition root injects
	// runtime/auth.NewBootstrapMiddleware here; Init() rejects a nil value
	// because the closed contract (auth.Route.BootstrapAuth) requires it.
	// Persistent operator authenticator on the single setup-driven admin path (ADR §D2).
	bootstrapAuth func(http.Handler) http.Handler

	// setupLockNil is the sentinel flag set when WithSetupLock receives a
	// bare-nil or typed-nil ports.SetupLockAcquirer. initValidate() checks both this
	// flag and setupLock itself so that explicitly passing nil cannot bypass
	// the required-dependency check.
	setupLockNil bool

	// setupLock is the REQUIRED serialization primitive for the admin-provisioning
	// path. PG composition roots wire accesspg.NewBundle(pool, txm, clk).SetupLock()
	// (pg_advisory_xact_lock);
	// memstore composition roots wire accesscore.NoopSetupLock{} because
	// memTxRunner.RunInTx already holds store.mu for the whole closure.
	// initValidate() rejects nil — the previous in-process sync.Mutex inside
	// adminprovision.Provisioner has been deleted.
	setupLock ports.SetupLockAcquirer

	// casProtocol is the CAS primitive for the ChangePassword path (S6).
	// Required — initValidate() rejects nil. Composition root injects via
	// WithCASProtocol; CAS-PROTOCOL-COMPOSITION-ROOT-01 archtest enforces that
	// cells never construct Protocol directly.
	casProtocol *cas.Protocol

	// passwordHasher is threaded to the setup + identitymanage services.
	// Defaults to credential.NewProductionHasher() (cost 12) in NewAccessCore;
	// tests/integration harnesses override via WithPasswordHasher to use
	// credential.NewTestHasher(bcrypt.MinCost) for speed. BCRYPT-COST-FUNNEL-01
	// guards that production never reaches the low-cost door.
	passwordHasher credential.Hasher

	// Slice handlers.
	// +slice:route:slice=policymanage,subPath=/policies
	policyHandler *policymanage.Handler

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
	// setupSvc is stored so RecordBootstrapAuthFail can be called from the
	// bootstrap auth-fail observer closure (Wave-1 #1423 event-based decoupling).
	// Set by initSetup(); nil until Init completes, so the observer must only be
	// invoked after startup (bootstrap middleware fires after HTTP servers start,
	// i.e. after Init has completed).
	setupSvc *setup.Service

	// Services exposed for composition (e.g. TokenVerifier, Authorizer).
	validateSvc *sessionvalidate.Service
	authzSvc    *authorizationdecide.Service

	// verifyRPCServer serves the grpc.auth.session.verify.v1 contract (first
	// platform-cell grpc service, PR-11 #1154). It carries no +slice:route marker —
	// grpc serve is derived by cellgen from slice.yaml contractUsages[role=serve]
	// (#1601), which resolves this field by "pointer-type package == sliceID"
	// (sessionverifyrpc) and emits the reg.GRPCService(...) call into cell_gen.go.
	verifyRPCServer *sessionverifyrpc.Server

	// +slice:route:slice=rbaccheck,subPath=/roles
	rbacHandler *rbaccheck.Handler
	// listRunMode is the cell-wide cursor run mode (fail-closed prod vs stale-key
	// demo fallback) shared by every list endpoint that uses the cursor codec
	// (rbaccheck, policymanage). Derived from the durability mode in cell_init.
	listRunMode query.RunMode

	// +slice:route:slice=rbacassign,listener=cell.InternalListener,subPath=/roles
	rbacAssignHandler *rbacassign.Handler

	configReceiveSvc *configreceive.Service

	rbacSessionConsumer *sessionlogout.Consumer
}

// NewAccessCore creates a new AccessCore Cell.
func NewAccessCore(clk clock.Clock, opts ...Option) *AccessCore {
	clock.MustHaveClock(clk, "accesscore.New")
	c := &AccessCore{
		BaseCell:       cell.MustNewBaseCell(loadCellMetadata()),
		clk:            clk,
		logger:         slog.Default(),
		passwordHasher: credential.NewProductionHasher(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// RecordBootstrapAuthFail records a bootstrap authentication failure by emitting
// event.auth.bootstrap-failed.v1 via the setup slice's outbox.
//
// Intended caller: the composition-root bootstrap-auth observer closure in
// cellmodules/accesscore (Wave-1 #1423). The observer is invoked by
// runtime/auth.NewBootstrapMiddleware after a 401/429 is written. Do not call
// this method from cells/ or runtime/ code.
//
// The setup service is only available after Init completes; the observer runs
// after HTTP servers start, so this is always safe.
//
// Returns an error if the setup service has not been initialized yet (Init not
// called) or if emitting the event fails. Callers (the observer closure) log
// the error and continue — the compliance chain is best-effort for the observer
// path, durable via outbox row persistence.
func (c *AccessCore) RecordBootstrapAuthFail(ctx context.Context, reason string, clientIPHash redaction.IPHash) error {
	if c.setupSvc == nil {
		return fmt.Errorf("accesscore: RecordBootstrapAuthFail called before Init")
	}
	return c.setupSvc.RecordBootstrapAuthFail(ctx, reason, clientIPHash)
}
