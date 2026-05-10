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
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// Compile-time interface check lives in cell_gen.go (DO NOT EDIT).

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

// WithOutboxDeps wires sealed outbox dependencies (CellPublisher +
// CellWriter). Composition roots construct each via
// outbox.WrapPublisherForCell / outbox.WrapWriterForCell. The framework
// composes them into an outbox.Emitter at Init() time via
// cell.ResolveCellEmitter.
//
// Accumulative: a nil argument leaves the previously-set value in place;
// multiple calls combine their non-nil arguments. Does NOT clear previous
// state — `WithOutboxDeps(nil, nil)` is a no-op, not a reset. Mutually
// exclusive with WithEmitter; Init() fails fast if both are set.
//
// AI-HARD per ADR cell-raw-infra-sealed-marker: the option signature
// rejects raw outbox.Publisher / outbox.Writer at compile time.
func WithOutboxDeps(pub outbox.CellPublisher, writer outbox.CellWriter) Option {
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

// WithTxManager sets the CellTxManager for transactional guarantees (L2
// atomicity). Composition roots construct via persistence.WrapForCell.
func WithTxManager(tx persistence.CellTxManager) Option {
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

// WithClock sets the time source for this Cell. Required — Init() panics via
// clock.MustHaveClock if not set. Composition root passes clock.Real(); tests
// inject a deterministic clock to control time-sensitive logic.
func WithClock(clk clock.Clock) Option {
	return func(c *AuditCore) { c.clk = clk }
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
// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1/audit
type AuditCore struct {
	*cell.BaseCell
	auditRepo    ports.AuditRepository
	archiveStore ports.ArchiveStore

	// Outbox wiring (see WithEmitter / WithOutboxDeps godoc). Sealed marker
	// types prevent any cell.go public Option from accepting raw
	// outbox.Publisher / outbox.Writer at compile time (ADR
	// cell-raw-infra-sealed-marker §D1).
	emitter             outbox.Emitter
	pendingOutboxPub    outbox.CellPublisher
	pendingOutboxWriter outbox.CellWriter

	txRunner        persistence.CellTxManager
	cursorCodec     *query.CursorCodec
	logger          *slog.Logger
	hmacKey         []byte
	metricsProvider metrics.Provider
	clk             clock.Clock

	// +slice:subscribe:slice=auditappend,topic=event.user.created.v1,handler=HandleEvent,group=auditcore
	// +slice:subscribe:slice=auditappend,topic=event.user.locked.v1,handler=HandleEvent,group=auditcore
	// +slice:subscribe:slice=auditappend,topic=event.user.updated.v1,handler=HandleEvent,group=auditcore
	// +slice:subscribe:slice=auditappend,topic=event.user.deleted.v1,handler=HandleEvent,group=auditcore
	// +slice:subscribe:slice=auditappend,topic=event.user.unlocked.v1,handler=HandleEvent,group=auditcore
	// +slice:subscribe:slice=auditappend,topic=event.session.created.v1,handler=HandleEvent,group=auditcore
	// +slice:subscribe:slice=auditappend,topic=event.session.revoked.v1,handler=HandleEvent,group=auditcore
	// +slice:subscribe:slice=auditappend,topic=event.config.entry-upserted.v1,handler=HandleEvent,group=auditcore
	// +slice:subscribe:slice=auditappend,topic=event.config.entry-deleted.v1,handler=HandleEvent,group=auditcore
	// +slice:subscribe:slice=auditappend,topic=event.config.version-published.v1,handler=HandleEvent,group=auditcore
	// +slice:subscribe:slice=auditappend,topic=event.config.rollback.v1,handler=HandleEvent,group=auditcore
	// +slice:subscribe:slice=auditappend,topic=event.role.assigned.v1,handler=HandleEvent,group=auditcore
	// +slice:subscribe:slice=auditappend,topic=event.role.revoked.v1,handler=HandleEvent,group=auditcore
	appendSvc *auditappend.Service

	verifySvc  *auditverify.Service
	archiveSvc *auditarchive.Service

	// +slice:route:slice=auditquery,subPath=
	queryHandler *auditquery.Handler
}

// NewAuditCore creates a new AuditCore Cell.
func NewAuditCore(opts ...Option) *AuditCore {
	c := &AuditCore{
		BaseCell: cell.MustNewBaseCell(loadCellMetadata()),
		logger:   slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// initInternal is the K#04 codegen escape hatch: business init that cannot
// be generated (emitter resolve, slice service construction, health probes).
// cell_gen.go::Init calls it after BaseCell.Init and before mounting the
// generated route-group and subscribe blocks. This is a permanent convention,
// not a transitional shim.
//
//nolint:unparam // ctx is a contract parameter; unused here, used by other cells
func (c *AuditCore) initInternal(ctx context.Context, reg cell.Registry) error {
	clock.MustHaveClock(c.clk, "auditcore.initInternal")
	if err := c.resolveHMACKey(reg.Config()); err != nil {
		return err
	}

	durabilityMode := reg.DurabilityMode()

	if err := c.resolveEmitter(durabilityMode); err != nil {
		return err
	}
	// resolveEmitter enforces the (OutboxWriter, TxRunner) pairing invariant
	// using the original c.txRunner; only after it succeeds do we install the
	// demoTxRunner fallback so slice constructors see a non-nil TxRunner.
	if c.txRunner == nil {
		c.logger.Warn("auditcore: using cell.DemoCellTxManager (demo mode)",
			slog.String("durability_mode", durabilityMode.String()))
		c.txRunner = cell.DemoCellTxManager()
	}
	// Guard: DemoTxRunner implements Nooper — reject it in DurabilityDurable mode
	// so that assemblies that forget to wire a real TxRunner fail at Init() time.
	if err := cell.CheckNotNoop(durabilityMode, "auditcore", c.txRunner); err != nil {
		return err
	}
	if err := c.initSlices(); err != nil {
		return err
	}
	// Default cursor codec for pagination if not injected. Durable mode
	// refuses the public demo-key fallback — an assembly that forgets to
	// wire a production codec must fail closed, not silently sign cursors
	// with a key that ships in the source tree.
	// ref: zeromicro/go-zero MustSetUp — fatal on insecure default config.
	if err := c.initCursorCodec(durabilityMode); err != nil {
		return err
	}
	if err := c.initQuerySlice(durabilityMode); err != nil {
		return err
	}

	// Route groups and subscriptions removed: cell_gen.go owns Init and renders them.

	// Register health probes (emitter fail-open rate checker).
	if hc, ok := c.emitter.(cell.HealthProber); ok {
		for k, v := range hc.Probes() {
			reg.Health(k, v)
		}
	}

	return nil
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
			Clock:             c.clk,
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
		return errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"auditcore: HMAC key is required (set via WithHMACKey or config audit.hmac_key)")
	}
	return nil
}

// initSlices constructs and registers the audit-append, audit-verify, and
// audit-archive slices. audit-query is initialized separately in initQuerySlice
// because it requires the cursor codec to be resolved first.
func (c *AuditCore) initSlices() error {
	// audit-append
	appendOpts := []auditappend.Option{auditappend.WithEmitter(c.emitter), auditappend.WithTxManager(c.txRunner)}
	appendSvc, err := auditappend.NewService(c.auditRepo, c.hmacKey, c.logger, c.clk, appendOpts...)
	if err != nil {
		return fmt.Errorf("auditappend: %w", err)
	}
	c.appendSvc = appendSvc
	// L3: 订阅 accesscore/configcore 跨 cell 事件，slice 级别可高于 cell 级别。
	c.AddSlice(cell.NewBaseSlice("auditappend", "auditcore", cell.L3))

	// audit-verify
	verifyOpts := []auditverify.Option{auditverify.WithEmitter(c.emitter), auditverify.WithTxManager(c.txRunner)}
	verifySvc, err := auditverify.NewService(c.auditRepo, c.hmacKey, c.logger, verifyOpts...)
	if err != nil {
		return fmt.Errorf("auditverify: %w", err)
	}
	c.verifySvc = verifySvc
	// L2: publishes event.audit.integrity-verified.v1 via transactional outbox.
	c.AddSlice(cell.NewBaseSlice("auditverify", "auditcore", cell.L2))

	// audit-archive (stub)
	c.archiveSvc = auditarchive.NewService()
	c.AddSlice(cell.NewBaseSlice("auditarchive", "auditcore", cell.L1))
	return nil
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

// initCursorCodec initializes the cursor codec with a demo key if not
// injected. In DurabilityDurable mode the demo fallback is refused — callers
// must inject a production codec via WithCursorCodec.
func (c *AuditCore) initCursorCodec(mode cell.DurabilityMode) error {
	if c.cursorCodec != nil {
		return nil
	}
	if mode == cell.DurabilityDurable {
		return errcode.New(errcode.KindInternal, errcode.ErrCellMissingCodec,
			"auditcore durable mode requires a cursor codec; "+
				"use WithCursorCodec(query.NewCursorCodec(secret)) — "+
				"the built-in demo key is public in the source tree")
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
