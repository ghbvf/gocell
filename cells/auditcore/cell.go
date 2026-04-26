// Package auditcore implements the auditcore Cell: tamper-evident audit log
// with hash chain, event consumption, integrity verification, and query.
package auditcore

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/cells/auditcore/internal/mem"
	"github.com/ghbvf/gocell/cells/auditcore/internal/ports"
	"github.com/ghbvf/gocell/cells/auditcore/slices/auditappend"
	"github.com/ghbvf/gocell/cells/auditcore/slices/auditarchive"
	"github.com/ghbvf/gocell/cells/auditcore/slices/auditquery"
	"github.com/ghbvf/gocell/cells/auditcore/slices/auditverify"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/kernel/wrapper"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// Topic constants — one per consumed event. FMT-18 resolves const string
// references at scan time so contract IDs can be written once here and
// reused as both map key and EventSpec argument without Sonar flagging
// duplicate literals.
const (
	topicUserCreated     = "event.user.created.v1"
	topicUserLocked      = "event.user.locked.v1"
	topicSessionCreated  = "event.session.created.v1"
	topicSessionRevoked  = "event.session.revoked.v1"
	topicConfigUpserted  = "event.config.entry-upserted.v1"
	topicConfigDeleted   = "event.config.entry-deleted.v1"
	topicConfigPublished = "event.config.version-published.v1"
	topicConfigRollback  = "event.config.rollback.v1"
	topicRoleAssigned    = "event.role.assigned.v1"
	topicRoleRevoked     = "event.role.revoked.v1"
)

// auditAppendSpecs maps each consumed topic to its wrapper.ContractSpec.
// Each value is a wrapper.EventSpec call so FMT-18's governance scan can
// resolve the contract id (via const reference) and cross-check it against
// contracts/event/**/contract.yaml.
//
// Adding or removing a topic MUST be mirrored in auditappend.Topics;
// RegisterSubscriptions fails at startup if the two drift.
var auditAppendSpecs = map[string]wrapper.ContractSpec{
	topicUserCreated:     wrapper.EventSpec(topicUserCreated, "amqp"),
	topicUserLocked:      wrapper.EventSpec(topicUserLocked, "amqp"),
	topicSessionCreated:  wrapper.EventSpec(topicSessionCreated, "amqp"),
	topicSessionRevoked:  wrapper.EventSpec(topicSessionRevoked, "amqp"),
	topicConfigUpserted:  wrapper.EventSpec(topicConfigUpserted, "amqp"),
	topicConfigDeleted:   wrapper.EventSpec(topicConfigDeleted, "amqp"),
	topicConfigPublished: wrapper.EventSpec(topicConfigPublished, "amqp"),
	topicConfigRollback:  wrapper.EventSpec(topicConfigRollback, "amqp"),
	topicRoleAssigned:    wrapper.EventSpec(topicRoleAssigned, "amqp"),
	topicRoleRevoked:     wrapper.EventSpec(topicRoleRevoked, "amqp"),
}

// Compile-time interface checks.
var (
	_ cell.Cell                  = (*AuditCore)(nil)
	_ cell.RouteGroupContributor = (*AuditCore)(nil)
	_ cell.EventRegistrar        = (*AuditCore)(nil)
	_ cell.HealthContributor     = (*AuditCore)(nil)
)

// Option configures an AuditCore Cell.
type Option func(*AuditCore)

// WithAuditRepository sets the AuditRepository.
func WithAuditRepository(r ports.AuditRepository) Option {
	return func(c *AuditCore) { c.auditRepo = r }
}

// WithArchiveStore sets the ArchiveStore.
func WithArchiveStore(s ports.ArchiveStore) Option {
	return func(c *AuditCore) { c.archiveStore = s }
}

// WithEmitter injects a pre-composed outbox.Emitter directly into the Cell.
// Preferred path for tests and for composition roots that have already built
// an Emitter.
//
// Mutually exclusive with WithOutboxDeps — setting both causes Init() to
// fail fast with ErrCellInvalidConfig. Durability for L2 slice decisions is
// derived from outbox.ReportDurable(emitter); emitters that do not implement
// DurabilityReporter are treated as non-durable.
//
// ref: kubernetes/client-go rest.RESTClientFor — factory composes the typed
// client; resulting struct does not retain raw config fields.
func WithEmitter(e outbox.Emitter) Option {
	return func(c *AuditCore) { c.emitter = e }
}

// WithOutboxDeps wires raw outbox dependencies (Publisher + Writer). The
// framework composes them into an outbox.Emitter at Init() time via
// cell.ResolveEmitter.
//
// Accumulative: a nil argument leaves the previously-set value in place;
// multiple calls combine their non-nil arguments. Does NOT clear previous
// state — `WithOutboxDeps(nil, nil)` is a no-op, not a reset. Mutually
// exclusive with WithEmitter; Init() fails fast if both are set.
func WithOutboxDeps(pub outbox.Publisher, writer outbox.Writer) Option {
	return func(c *AuditCore) {
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
	return func(c *AuditCore) { c.logger = l }
}

// WithTxManager sets the TxRunner for transactional guarantees (L2 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(c *AuditCore) { c.txRunner = tx }
}

// WithHMACKey sets the HMAC key for hash chain operations.
func WithHMACKey(key []byte) Option {
	return func(c *AuditCore) { c.hmacKey = key }
}

// WithMetricsProvider sets the metrics provider used by the DirectEmitter in
// demo mode. Required when WithOutboxDeps sets a publisher without a real
// outboxWriter. Pass metrics.NopProvider{} explicitly in tests.
func WithMetricsProvider(p metrics.Provider) Option {
	return func(c *AuditCore) { c.metricsProvider = p }
}

// WithCursorCodec sets the cursor codec for pagination.
func WithCursorCodec(codec *query.CursorCodec) Option {
	return func(c *AuditCore) { c.cursorCodec = codec }
}

// WithInMemoryDefaults configures in-memory repositories for development
// and testing. Not suitable for production use.
func WithInMemoryDefaults() Option {
	return func(c *AuditCore) {
		c.auditRepo = mem.NewAuditRepository()
		c.archiveStore = mem.NewArchiveStore()
	}
}

// AuditCore is the auditcore Cell implementation.
type AuditCore struct {
	*cell.BaseCell
	auditRepo    ports.AuditRepository
	archiveStore ports.ArchiveStore

	// Outbox wiring (see WithEmitter / WithOutboxDeps godoc). Private;
	// archtest OUTBOX-CELL-01 forbids exported raw outbox options.
	emitter             outbox.Emitter
	pendingOutboxPub    outbox.Publisher
	pendingOutboxWriter outbox.Writer

	txRunner        persistence.TxRunner
	cursorCodec     *query.CursorCodec
	logger          *slog.Logger
	hmacKey         []byte
	metricsProvider metrics.Provider

	// Slice services.
	appendSvc    *auditappend.Service
	verifySvc    *auditverify.Service
	archiveSvc   *auditarchive.Service
	queryHandler *auditquery.Handler
}

// HealthCheckers implements cell.HealthContributor. Aggregates the outbox
// emitter's HealthCheckers (currently fail-open drop rate → degraded signal)
// so /readyz surfaces "audit events are being lost in fail-open path"
// without polluting the cell's primary Cell.Health() signal.
//
// Note: auditcore uses DirectPublishFailClosed, so the fail-open checker
// will never trip in normal operation; the checker is still wired for
// consistency and forward-compatibility.
//
// The emitter checker (outbox-failopen-rate.auditcore) is enabled by default
// at a 5% threshold; it returns cell.ErrDegraded when the fail-open drop ratio
// sustained between two /readyz probes exceeds that threshold. Disable via
// outbox.WithFailOpenRateThreshold(0) when constructing the emitter.
func (c *AuditCore) HealthCheckers() map[string]func(context.Context) error {
	checkers := make(map[string]func(context.Context) error)
	if hc, ok := c.emitter.(cell.HealthContributor); ok {
		for k, v := range hc.HealthCheckers() {
			if _, dup := checkers[k]; dup {
				slog.Error("auditcore: duplicate health checker name; emitter checker dropped",
					slog.String("checker", k),
					slog.String("source", "outbox-emitter"))
				continue
			}
			checkers[k] = v
		}
	}
	return checkers
}

// NewAuditCore creates a new AuditCore Cell.
func NewAuditCore(opts ...Option) *AuditCore {
	c := &AuditCore{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{
			ID:   "auditcore",
			Type: cell.CellTypeCore,
			// L2: 对外 contract (audit.appended, integrity-verified) 都是本地事务 + outbox 发布。
			// 订阅跨 cell 事件是 slice 级行为 (audit-append L3)，不升 cell 级别 — 同 configcore 模式。
			ConsistencyLevel: cell.L2,
			Owner:            cell.Owner{Team: "platform", Role: "audit-owner"},
			Schema:           cell.SchemaConfig{Primary: "audit_entries"},
			Verify:           cell.CellVerify{Smoke: []string{"auditcore/smoke"}},
		}),
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Init constructs all 4 slices.
func (c *AuditCore) Init(ctx context.Context, deps cell.Dependencies) error {
	if err := c.resolveHMACKey(deps.Config); err != nil {
		return err
	}
	if err := c.BaseCell.Init(ctx, deps); err != nil {
		return err
	}
	if err := c.resolveEmitter(deps.DurabilityMode); err != nil {
		return err
	}
	c.initSlices()
	// Default cursor codec for pagination if not injected. Durable mode
	// refuses the public demo-key fallback — an assembly that forgets to
	// wire a production codec must fail closed, not silently sign cursors
	// with a key that ships in the source tree.
	// ref: zeromicro/go-zero MustSetUp — fatal on insecure default config.
	if err := c.initCursorCodec(deps.DurabilityMode); err != nil {
		return err
	}
	return c.initQuerySlice(deps.DurabilityMode)
}

// resolveEmitter delegates to cell.ResolveCellEmitter (mutual exclusion +
// WithEmitter durable guard + ResolveEmitter delegation + L2 non-durable
// warn) and clears the pending outbox dep fields.
//
// auditcore uses DirectPublishFailClosed: audit-chain events (audit.appended,
// integrity-verified) are the source of truth for compliance; publisher
// failure must surface to the caller so ops notices outages instead of
// silently losing events. Opt-in fail-open is per-entry via
// outbox.Entry.FailurePolicy, and archtest OUTBOX-TOPIC-FAILOPEN-01 bans it
// for audit.* topics.
func (c *AuditCore) resolveEmitter(mode cell.DurabilityMode) error {
	outcome, err := cell.ResolveCellEmitter(cell.CellEmitterInputs{
		EmitterConfig: cell.EmitterConfig{
			CellID:            "auditcore",
			Mode:              mode,
			Publisher:         c.pendingOutboxPub,
			OutboxWriter:      c.pendingOutboxWriter,
			TxRunner:          c.txRunner,
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
	c.emitter = outcome.Emitter
	c.pendingOutboxPub = nil
	c.pendingOutboxWriter = nil
	c.txRunner = persistence.RunnerOrNoop(c.txRunner)
	return nil
}

// resolveHMACKey populates c.hmacKey from config if not set via option.
func (c *AuditCore) resolveHMACKey(cfg map[string]any) error {
	if len(c.hmacKey) == 0 {
		if raw, ok := cfg["audit.hmac_key"]; ok {
			if s, ok := raw.(string); ok && s != "" {
				c.hmacKey = []byte(s)
			}
		}
	}
	if len(c.hmacKey) == 0 {
		return errcode.New(errcode.ErrValidationFailed,
			"auditcore: HMAC key is required (set via WithHMACKey or config audit.hmac_key)")
	}
	return nil
}

// initSlices constructs and registers the audit-append, audit-verify, and
// audit-archive slices. audit-query is initialised separately in initQuerySlice
// because it requires the cursor codec to be resolved first.
func (c *AuditCore) initSlices() {
	// audit-append
	appendOpts := []auditappend.Option{auditappend.WithEmitter(c.emitter), auditappend.WithTxManager(c.txRunner)}
	c.appendSvc = auditappend.NewService(c.auditRepo, c.hmacKey, c.logger, appendOpts...)
	// L3: 订阅 accesscore/configcore 跨 cell 事件，slice 级别可高于 cell 级别。
	c.AddSlice(cell.NewBaseSlice("auditappend", "auditcore", cell.L3))

	// audit-verify
	verifyOpts := []auditverify.Option{auditverify.WithEmitter(c.emitter), auditverify.WithTxManager(c.txRunner)}
	c.verifySvc = auditverify.NewService(c.auditRepo, c.hmacKey, c.logger, verifyOpts...)
	// L2: publishes event.audit.integrity-verified.v1 via transactional outbox.
	c.AddSlice(cell.NewBaseSlice("auditverify", "auditcore", cell.L2))

	// audit-archive (stub)
	c.archiveSvc = auditarchive.NewService()
	c.AddSlice(cell.NewBaseSlice("auditarchive", "auditcore", cell.L1))
}

// initQuerySlice constructs the audit-query handler slice. Must be called after
// initCursorCodec so that c.cursorCodec is set.
func (c *AuditCore) initQuerySlice(mode cell.DurabilityMode) error {
	querySvc, err := auditquery.NewService(c.auditRepo, c.cursorCodec, c.logger,
		query.RunModeForDemo(mode == cell.DurabilityDemo))
	if err != nil {
		return fmt.Errorf("audit-query: %w", err)
	}
	c.queryHandler = auditquery.NewHandler(querySvc)
	c.AddSlice(cell.NewBaseSlice("auditquery", "auditcore", cell.L0))
	return nil
}

// initCursorCodec initialises the cursor codec with a demo key if not
// injected. In DurabilityDurable mode the demo fallback is refused — callers
// must inject a production codec via WithCursorCodec.
func (c *AuditCore) initCursorCodec(mode cell.DurabilityMode) error {
	if c.cursorCodec != nil {
		return nil
	}
	if mode == cell.DurabilityDurable {
		return errcode.New(errcode.ErrCellMissingCodec,
			"auditcore durable mode requires a cursor codec; use WithCursorCodec(query.NewCursorCodec(secret)) — the built-in demo key is public in the source tree")
	}
	// Each cell uses a distinct demo key to prevent cross-cell cursor reuse in demo mode.
	codec, err := query.NewCursorCodec([]byte("gocell-demo-AUDIT--CORE-key-32!!"))
	if err != nil {
		return err
	}
	c.cursorCodec = codec
	c.logger.Warn("auditcore: using default cursor codec (demo mode)",
		slog.String("cell", c.ID()))
	return nil
}

// RouteGroups declares auditcore's HTTP route groups on the PrimaryListener.
//
// ref: go-zero rest/server.go AddRoutes — per-listener route declaration.
func (c *AuditCore) RouteGroups() []cell.RouteGroup {
	return []cell.RouteGroup{
		{
			Listener: cell.PrimaryListener,
			Prefix:   "/api/v1/audit",
			Register: func(mux cell.RouteMux) {
				c.queryHandler.RegisterRoutes(mux)
			},
		},
	}
}

// RegisterSubscriptions declares event subscriptions for all audit topics.
// Each topic has a matching wrapper.ContractSpec so every consumed entry
// emits a CONSUME span annotated with gocell.contract.id; the Router
// manages goroutine lifecycle and setup-error detection.
func (c *AuditCore) RegisterSubscriptions(r cell.EventRouter) error {
	handler := outbox.WrapLegacyHandler(c.appendSvc.HandleEvent)
	for _, topic := range auditappend.Topics {
		spec, ok := auditAppendSpecs[topic]
		if !ok {
			return fmt.Errorf("auditcore: missing ContractSpec for topic %q — "+
				"auditAppendSpecs and auditappend.Topics must stay in sync", topic)
		}
		r.AddContractHandler(spec, handler, "auditcore")
	}
	return nil
}
