// Package configcore implements the config-core Cell: configuration management
// with versioning, publishing, rollback, and feature flag evaluation.
package configcore

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/cells/config-core/internal/ports"
	"github.com/ghbvf/gocell/cells/config-core/slices/configpublish"
	"github.com/ghbvf/gocell/cells/config-core/slices/configread"
	"github.com/ghbvf/gocell/cells/config-core/slices/configsubscribe"
	"github.com/ghbvf/gocell/cells/config-core/slices/configwrite"
	"github.com/ghbvf/gocell/cells/config-core/slices/featureflag"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
)

// Compile-time interface checks.
var (
	_ cell.Cell          = (*ConfigCore)(nil)
	_ cell.HTTPRegistrar = (*ConfigCore)(nil)
	_ cell.EventRegistrar = (*ConfigCore)(nil)
)

// Option configures a ConfigCore Cell.
type Option func(*ConfigCore)

// WithConfigRepository sets the ConfigRepository.
func WithConfigRepository(r ports.ConfigRepository) Option {
	return func(c *ConfigCore) { c.configRepo = r }
}

// WithFlagRepository sets the FlagRepository.
func WithFlagRepository(r ports.FlagRepository) Option {
	return func(c *ConfigCore) { c.flagRepo = r }
}

// WithPublisher sets the outbox Publisher.
func WithPublisher(p outbox.Publisher) Option {
	return func(c *ConfigCore) { c.publisher = p }
}

// WithLogger sets the structured logger.
func WithLogger(l *slog.Logger) Option {
	return func(c *ConfigCore) { c.logger = l }
}

// WithOutboxWriter sets the outbox.Writer for transactional event publishing.
func WithOutboxWriter(w outbox.Writer) Option {
	return func(c *ConfigCore) { c.outboxWriter = w }
}

// WithTxManager sets the TxRunner for transactional guarantees (L2 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(c *ConfigCore) { c.txRunner = tx }
}

// WithCursorCodec sets the cursor codec for pagination.
func WithCursorCodec(codec *query.CursorCodec) Option {
	return func(c *ConfigCore) { c.cursorCodec = codec }
}

// WithInMemoryDefaults configures in-memory repositories for development
// and testing. Not suitable for production use.
func WithInMemoryDefaults() Option {
	return func(c *ConfigCore) {
		c.configRepo = mem.NewConfigRepository()
		c.flagRepo = mem.NewFlagRepository()
	}
}

// ConfigCore is the config-core Cell implementation.
type ConfigCore struct {
	*cell.BaseCell
	configRepo   ports.ConfigRepository
	flagRepo     ports.FlagRepository
	publisher    outbox.Publisher
	outboxWriter outbox.Writer
	txRunner     persistence.TxRunner
	cursorCodec  *query.CursorCodec
	logger       *slog.Logger

	// Slice services and handlers.
	writeHandler     *configwrite.Handler
	readHandler      *configread.Handler
	publishHandler   *configpublish.Handler
	flagHandler      *featureflag.Handler
	subscribeSvc     *configsubscribe.Service
}

// NewConfigCore creates a new ConfigCore Cell.
func NewConfigCore(opts ...Option) *ConfigCore {
	c := &ConfigCore{
		BaseCell: cell.NewBaseCell(cell.CellMetadata{
			ID:               "config-core",
			Type:             cell.CellTypeCore,
			ConsistencyLevel: cell.L2,
			Owner:            cell.Owner{Team: "platform", Role: "config-owner"},
			Schema:           cell.SchemaConfig{Primary: "config_entries"},
			Verify:           cell.CellVerify{Smoke: []string{"config-core/smoke"}},
		}),
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Init constructs all 5 slices and registers them.
func (c *ConfigCore) Init(ctx context.Context, deps cell.Dependencies) error {
	if err := c.BaseCell.Init(ctx, deps); err != nil {
		return err
	}

	// Fail-fast: outboxWriter and txRunner must be both present or both absent (XOR constraint).
	// Both present = durable mode (L2 atomicity). Both absent = demo/in-memory mode.
	if (c.outboxWriter == nil) != (c.txRunner == nil) {
		return errcode.New(errcode.ErrCellMissingOutbox,
			"config-core durable mode requires both outboxWriter and txRunner")
	}

	// Durable mode: reject noop implementations.
	if err := cell.CheckNotNoop(deps.DurabilityMode, "config-core", c.outboxWriter, c.txRunner, c.publisher); err != nil {
		return err
	}

	// Demo mode: both nil → require publisher for degraded event delivery.
	if c.outboxWriter == nil && c.txRunner == nil {
		if c.publisher == nil {
			return errcode.New(errcode.ErrCellMissingOutbox,
				"config-core requires publisher or outbox writer; use WithPublisher(outbox.DiscardPublisher{}) for demo mode")
		}
		if c.ConsistencyLevel() >= cell.L2 {
			c.logger.Warn("config-core: running without outboxWriter+txRunner, L2 transactional atomicity not guaranteed (demo mode)",
				slog.String("cell", c.ID()),
				slog.Int("consistency_level", int(c.ConsistencyLevel())))
		}
	}

	// config-write slice
	var writeOpts []configwrite.Option
	if c.outboxWriter != nil {
		writeOpts = append(writeOpts, configwrite.WithOutboxWriter(c.outboxWriter))
	}
	if c.txRunner != nil {
		writeOpts = append(writeOpts, configwrite.WithTxManager(c.txRunner))
	}
	writeSvc := configwrite.NewService(c.configRepo, c.publisher, c.logger, writeOpts...)
	c.writeHandler = configwrite.NewHandler(writeSvc)
	c.AddSlice(cell.NewBaseSlice("config-write", "config-core", cell.L2))

	// Default cursor codec for pagination if not injected.
	if c.cursorCodec == nil {
		// Each cell uses a distinct demo key to prevent cross-cell cursor reuse in demo mode.
		codec, err := query.NewCursorCodec([]byte("gocell-demo-CONFIG-CORE-key-32!!"))
		if err != nil {
			return err
		}
		c.cursorCodec = codec
		c.logger.Warn("config-core: using default cursor codec (demo mode)",
			slog.String("cell", c.ID()))
	}

	// config-read slice
	readSvc := configread.NewService(c.configRepo, c.cursorCodec, c.logger)
	c.readHandler = configread.NewHandler(readSvc)
	c.AddSlice(cell.NewBaseSlice("config-read", "config-core", cell.L0))

	// config-publish slice
	var publishOpts []configpublish.Option
	if c.outboxWriter != nil {
		publishOpts = append(publishOpts, configpublish.WithOutboxWriter(c.outboxWriter))
	}
	if c.txRunner != nil {
		publishOpts = append(publishOpts, configpublish.WithTxManager(c.txRunner))
	}
	publishSvc := configpublish.NewService(c.configRepo, c.publisher, c.logger, publishOpts...)
	c.publishHandler = configpublish.NewHandler(publishSvc)
	c.AddSlice(cell.NewBaseSlice("config-publish", "config-core", cell.L2))

	// config-subscribe slice
	c.subscribeSvc = configsubscribe.NewService(c.logger)
	c.AddSlice(cell.NewBaseSlice("config-subscribe", "config-core", cell.L3))

	// feature-flag slice
	flagSvc := featureflag.NewService(c.flagRepo, c.cursorCodec, c.logger)
	c.flagHandler = featureflag.NewHandler(flagSvc)
	c.AddSlice(cell.NewBaseSlice("feature-flag", "config-core", cell.L0))

	return nil
}

// RegisterRoutes registers HTTP routes for config-core.
func (c *ConfigCore) RegisterRoutes(mux cell.RouteMux) {
	mux.Route("/api/v1", func(v1 cell.RouteMux) {
		// Config CRUD + publish/rollback under /api/v1/config.
		v1.Route("/config", func(cfg cell.RouteMux) {
			// config-read
			cfg.Handle("GET /", http.HandlerFunc(c.readHandler.HandleList))
			cfg.Handle("GET /{key}", http.HandlerFunc(c.readHandler.HandleGet))
			// config-write
			cfg.Handle("POST /", http.HandlerFunc(c.writeHandler.HandleCreate))
			cfg.Handle("PUT /{key}", http.HandlerFunc(c.writeHandler.HandleUpdate))
			cfg.Handle("DELETE /{key}", http.HandlerFunc(c.writeHandler.HandleDelete))
			// config-publish
			cfg.Handle("POST /{key}/publish", http.HandlerFunc(c.publishHandler.HandlePublish))
			cfg.Handle("POST /{key}/rollback", http.HandlerFunc(c.publishHandler.HandleRollback))
		})

		// feature-flag: /api/v1/flags
		v1.Route("/flags", func(f cell.RouteMux) {
			f.Handle("GET /", http.HandlerFunc(c.flagHandler.HandleList))
			f.Handle("GET /{key}", http.HandlerFunc(c.flagHandler.HandleGet))
			f.Handle("POST /{key}/evaluate", http.HandlerFunc(c.flagHandler.HandleEvaluate))
		})
	})
}

// RegisterSubscriptions declares event subscriptions for config-core.
// The Router manages goroutine lifecycle and setup-error detection.
func (c *ConfigCore) RegisterSubscriptions(r cell.EventRouter) error {
	handler := outbox.WrapLegacyHandler(c.subscribeSvc.HandleEvent)
	r.AddHandler(configsubscribe.TopicConfigChanged, handler, "config-core")
	return nil
}
