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
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// Compile-time interface checks.
var (
	_ cell.Cell           = (*AuditCore)(nil)
	_ cell.HTTPRegistrar  = (*AuditCore)(nil)
	_ cell.EventRegistrar = (*AuditCore)(nil)
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

// WithPublisher sets the outbox Publisher.
func WithPublisher(p outbox.Publisher) Option {
	return func(c *AuditCore) { c.publisher = p }
}

// WithLogger sets the structured logger.
func WithLogger(l *slog.Logger) Option {
	return func(c *AuditCore) { c.logger = l }
}

// WithOutboxWriter sets the outbox.Writer for transactional event publishing.
func WithOutboxWriter(w outbox.Writer) Option {
	return func(c *AuditCore) { c.outboxWriter = w }
}

// WithTxManager sets the TxRunner for transactional guarantees (L2 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(c *AuditCore) { c.txRunner = tx }
}

// WithHMACKey sets the HMAC key for hash chain operations.
func WithHMACKey(key []byte) Option {
	return func(c *AuditCore) { c.hmacKey = key }
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
	publisher    outbox.Publisher
	outboxWriter outbox.Writer
	txRunner     persistence.TxRunner
	emitter      outbox.Emitter
	cursorCodec  *query.CursorCodec
	logger       *slog.Logger
	hmacKey      []byte

	// Slice services.
	appendSvc    *auditappend.Service
	verifySvc    *auditverify.Service
	archiveSvc   *auditarchive.Service
	queryHandler *auditquery.Handler
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
	outcome, err := cell.ResolveEmitter(cell.EmitterConfig{
		CellID:       "auditcore",
		Mode:         deps.DurabilityMode,
		Publisher:    c.publisher,
		OutboxWriter: c.outboxWriter,
		TxRunner:     c.txRunner,
		Logger:       c.logger,
		// auditcore runs DirectEmitter fail-open under both modes — audit
		// events are reconcile-replayable (append-only log is the source of
		// truth), so dropping a publisher failure is acceptable.
		// ref: kernel/cell.DirectPublishModeForDurability (PR-A5c / A5a-R4).
		DirectPublishMode: cell.DirectPublishModeForDurability(
			deps.DurabilityMode,
			outbox.DirectPublishFailOpen,
			outbox.DirectPublishFailOpen,
		),
	})
	if err != nil {
		return err
	}
	c.txRunner = persistence.RunnerOrNoop(c.txRunner)
	c.emitter = outcome.Emitter
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

// RegisterRoutes registers HTTP routes for auditcore.
func (c *AuditCore) RegisterRoutes(mux cell.RouteMux) {
	mux.Route("/api/v1/audit", func(sub cell.RouteMux) {
		c.queryHandler.RegisterRoutes(sub)
	})
}

// RegisterSubscriptions declares event subscriptions for all audit topics.
// The Router manages goroutine lifecycle and setup-error detection.
func (c *AuditCore) RegisterSubscriptions(r cell.EventRouter) error {
	handler := outbox.WrapLegacyHandler(c.appendSvc.HandleEvent)
	for _, topic := range auditappend.Topics {
		r.AddHandler(topic, handler, "auditcore")
	}
	return nil
}
