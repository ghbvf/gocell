// Package auditcore implements the audit-core Cell: tamper-evident audit log
// with hash chain, event consumption, integrity verification, and query.
package auditcore

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/cells/audit-core/internal/mem"
	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
	"github.com/ghbvf/gocell/cells/audit-core/slices/auditappend"
	"github.com/ghbvf/gocell/cells/audit-core/slices/auditarchive"
	"github.com/ghbvf/gocell/cells/audit-core/slices/auditquery"
	"github.com/ghbvf/gocell/cells/audit-core/slices/auditverify"
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

// AuditCore is the audit-core Cell implementation.
type AuditCore struct {
	*cell.BaseCell
	auditRepo    ports.AuditRepository
	archiveStore ports.ArchiveStore
	publisher    outbox.Publisher
	outboxWriter outbox.Writer
	txRunner     persistence.TxRunner
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
			ID:               "audit-core",
			Type:             cell.CellTypeCore,
			// L2: 对外 contract (audit.appended, integrity-verified) 都是本地事务 + outbox 发布。
			// 订阅跨 cell 事件是 slice 级行为 (audit-append L3)，不升 cell 级别 — 同 config-core 模式。
			ConsistencyLevel: cell.L2,
			Owner:            cell.Owner{Team: "platform", Role: "audit-owner"},
			Schema:           cell.SchemaConfig{Primary: "audit_entries"},
			Verify:           cell.CellVerify{Smoke: []string{"audit-core/smoke"}},
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
	// Resolve HMAC key from Dependencies.Config if not set via option.
	if len(c.hmacKey) == 0 {
		if raw, ok := deps.Config["audit.hmac_key"]; ok {
			if s, ok := raw.(string); ok && s != "" {
				c.hmacKey = []byte(s)
			}
		}
	}
	if len(c.hmacKey) == 0 {
		return errcode.New(errcode.ErrValidationFailed,
			"audit-core: HMAC key is required (set via WithHMACKey or config audit.hmac_key)")
	}

	if err := c.BaseCell.Init(ctx, deps); err != nil {
		return err
	}

	// Fail-fast: outboxWriter and txRunner must be both present or both absent (XOR constraint).
	// Both present = durable mode (L2 atomicity). Both absent = demo/in-memory mode.
	if (c.outboxWriter == nil) != (c.txRunner == nil) {
		return errcode.New(errcode.ErrCellMissingOutbox,
			"audit-core durable mode requires both outboxWriter and txRunner")
	}

	// Demo mode: both nil → require publisher for degraded event delivery.
	if c.outboxWriter == nil && c.txRunner == nil {
		if c.publisher == nil {
			return errcode.New(errcode.ErrCellMissingOutbox,
				"audit-core requires publisher or outbox writer; use WithPublisher(outbox.DiscardPublisher{}) for demo mode")
		}
		if c.ConsistencyLevel() >= cell.L2 {
			c.logger.Warn("audit-core: running without outboxWriter+txRunner, L2 transactional atomicity not guaranteed (demo mode)",
				slog.String("cell", c.ID()),
				slog.Int("consistency_level", int(c.ConsistencyLevel())))
		}
	}

	// audit-append
	var appendOpts []auditappend.Option
	if c.outboxWriter != nil {
		appendOpts = append(appendOpts, auditappend.WithOutboxWriter(c.outboxWriter))
	}
	if c.txRunner != nil {
		appendOpts = append(appendOpts, auditappend.WithTxManager(c.txRunner))
	}
	c.appendSvc = auditappend.NewService(c.auditRepo, c.hmacKey, c.publisher, c.logger, appendOpts...)
	// L3: 订阅 access-core/config-core 跨 cell 事件，slice 级别可高于 cell 级别。
	c.AddSlice(cell.NewBaseSlice("audit-append", "audit-core", cell.L3))

	// audit-verify
	var verifyOpts []auditverify.Option
	if c.outboxWriter != nil {
		verifyOpts = append(verifyOpts, auditverify.WithOutboxWriter(c.outboxWriter))
	}
	if c.txRunner != nil {
		verifyOpts = append(verifyOpts, auditverify.WithTxManager(c.txRunner))
	}
	c.verifySvc = auditverify.NewService(c.auditRepo, c.hmacKey, c.publisher, c.logger, verifyOpts...)
	c.AddSlice(cell.NewBaseSlice("audit-verify", "audit-core", cell.L0))

	// audit-archive (stub)
	c.archiveSvc = auditarchive.NewService()
	c.AddSlice(cell.NewBaseSlice("audit-archive", "audit-core", cell.L1))

	// Default cursor codec for pagination if not injected.
	if err := c.initCursorCodec(); err != nil {
		return err
	}

	// audit-query
	querySvc := auditquery.NewService(c.auditRepo, c.cursorCodec, c.logger)
	c.queryHandler = auditquery.NewHandler(querySvc)
	c.AddSlice(cell.NewBaseSlice("audit-query", "audit-core", cell.L0))

	return nil
}

// initCursorCodec initialises the cursor codec with a demo key if not injected.
func (c *AuditCore) initCursorCodec() error {
	if c.cursorCodec != nil {
		return nil
	}
	// Each cell uses a distinct demo key to prevent cross-cell cursor reuse in demo mode.
	codec, err := query.NewCursorCodec([]byte("gocell-demo-AUDIT--CORE-key-32!!"))
	if err != nil {
		return err
	}
	c.cursorCodec = codec
	c.logger.Warn("audit-core: using default cursor codec (demo mode)",
		slog.String("cell", c.ID()))
	return nil
}

// RegisterRoutes registers HTTP routes for audit-core.
func (c *AuditCore) RegisterRoutes(mux cell.RouteMux) {
	mux.Route("/api/v1/audit", func(sub cell.RouteMux) {
		sub.Handle("GET /entries", http.HandlerFunc(c.queryHandler.HandleQuery))
	})
}

// RegisterSubscriptions declares event subscriptions for all audit topics.
// The Router manages goroutine lifecycle and setup-error detection.
func (c *AuditCore) RegisterSubscriptions(r cell.EventRouter) error {
	handler := outbox.WrapLegacyHandler(c.appendSvc.HandleEvent)
	for _, topic := range auditappend.Topics {
		r.AddHandler(topic, handler, "audit-core")
	}
	return nil
}
