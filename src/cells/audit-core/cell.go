// Package auditcore implements the audit-core Cell: tamper-evident audit log
// with hash chain, event consumption, integrity verification, and query.
package auditcore

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/ghbvf/gocell/cells/audit-core/internal/mem"
	"github.com/ghbvf/gocell/cells/audit-core/internal/ports"
	"github.com/ghbvf/gocell/cells/audit-core/slices/auditappend"
	"github.com/ghbvf/gocell/cells/audit-core/slices/auditarchive"
	"github.com/ghbvf/gocell/cells/audit-core/slices/auditquery"
	"github.com/ghbvf/gocell/cells/audit-core/slices/auditverify"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/pkg/errcode"
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

// WithHMACKey sets the HMAC key for hash chain operations.
func WithHMACKey(key []byte) Option {
	return func(c *AuditCore) { c.hmacKey = key }
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
	logger       *slog.Logger
	hmacKey      []byte

	// Slice services.
	appendSvc  *auditappend.Service
	verifySvc  *auditverify.Service
	archiveSvc *auditarchive.Service
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

	// Fail-fast: L2+ Cell requires outboxWriter for transactional event publishing.
	if c.ConsistencyLevel() >= cell.L2 && c.outboxWriter == nil {
		slog.Warn("audit-core: outboxWriter not injected, L2 consistency not guaranteed")
		return errcode.New(errcode.ErrCellMissingOutbox, "audit-core (L2) requires outboxWriter injection")
	}

	// audit-append
	var appendOpts []auditappend.Option
	if c.outboxWriter != nil {
		appendOpts = append(appendOpts, auditappend.WithOutboxWriter(c.outboxWriter))
	}
	c.appendSvc = auditappend.NewService(c.auditRepo, c.hmacKey, c.publisher, c.logger, appendOpts...)
	// L3: 订阅 access-core/config-core 跨 cell 事件，slice 级别可高于 cell 级别。
	c.AddSlice(cell.NewBaseSlice("audit-append", "audit-core", cell.L3))

	// audit-verify
	var verifyOpts []auditverify.Option
	if c.outboxWriter != nil {
		verifyOpts = append(verifyOpts, auditverify.WithOutboxWriter(c.outboxWriter))
	}
	c.verifySvc = auditverify.NewService(c.auditRepo, c.hmacKey, c.publisher, c.logger, verifyOpts...)
	c.AddSlice(cell.NewBaseSlice("audit-verify", "audit-core", cell.L0))

	// audit-archive (stub)
	c.archiveSvc = auditarchive.NewService()
	c.AddSlice(cell.NewBaseSlice("audit-archive", "audit-core", cell.L1))

	// audit-query
	querySvc := auditquery.NewService(c.auditRepo, c.logger)
	c.queryHandler = auditquery.NewHandler(querySvc)
	c.AddSlice(cell.NewBaseSlice("audit-query", "audit-core", cell.L0))

	return nil
}

// RegisterRoutes registers HTTP routes for audit-core.
func (c *AuditCore) RegisterRoutes(mux cell.RouteMux) {
	mux.Route("/api/v1/audit", func(sub cell.RouteMux) {
		sub.Handle("GET /entries", http.HandlerFunc(c.queryHandler.HandleQuery))
	})
}

// RegisterSubscriptions registers event subscriptions for all 6 topics.
// Returns an error if any subscription fails during setup (e.g., missing DLX).
// Long-running consumption loops are started in background goroutines.
func (c *AuditCore) RegisterSubscriptions(sub outbox.Subscriber) error {
	handler := outbox.WrapLegacyHandler(c.appendSvc.HandleEvent)
	for _, topic := range auditappend.Topics {
		topic := topic
		errCh := make(chan error, 1)
		go func() {
			ctx := context.Background()
			errCh <- sub.Subscribe(ctx, topic, handler)
		}()

		// Subscribe returns immediately on config errors (DLX missing, closed).
		// On success it blocks — a short wait distinguishes the two.
		select {
		case err := <-errCh:
			if err != nil {
				return fmt.Errorf("audit-core: subscription setup failed for topic %s: %w", topic, err)
			}
			// Subscribe returned nil without blocking — clean shutdown, not an error.
		case <-time.After(100 * time.Millisecond):
			// Subscribe is blocking (consuming) — setup succeeded.
		}
	}
	return nil
}
