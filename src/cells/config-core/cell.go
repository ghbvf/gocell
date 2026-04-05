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
	configRepo ports.ConfigRepository
	flagRepo   ports.FlagRepository
	publisher  outbox.Publisher
	logger     *slog.Logger

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

	// config-write slice
	writeSvc := configwrite.NewService(c.configRepo, c.publisher, c.logger)
	c.writeHandler = configwrite.NewHandler(writeSvc)
	c.AddSlice(cell.NewBaseSlice("config-write", "config-core", cell.L2))

	// config-read slice
	readSvc := configread.NewService(c.configRepo, c.logger)
	c.readHandler = configread.NewHandler(readSvc)
	c.AddSlice(cell.NewBaseSlice("config-read", "config-core", cell.L0))

	// config-publish slice
	publishSvc := configpublish.NewService(c.configRepo, c.publisher, c.logger)
	c.publishHandler = configpublish.NewHandler(publishSvc)
	c.AddSlice(cell.NewBaseSlice("config-publish", "config-core", cell.L2))

	// config-subscribe slice
	c.subscribeSvc = configsubscribe.NewService(c.logger)
	c.AddSlice(cell.NewBaseSlice("config-subscribe", "config-core", cell.L3))

	// feature-flag slice
	flagSvc := featureflag.NewService(c.flagRepo, c.logger)
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

// RegisterSubscriptions registers event subscriptions for config-core.
func (c *ConfigCore) RegisterSubscriptions(sub outbox.Subscriber) {
	go func() {
		ctx := context.Background()
		if err := sub.Subscribe(ctx, configsubscribe.TopicConfigChanged, c.subscribeSvc.HandleEvent); err != nil {
			c.logger.Error("config-subscribe: subscription ended",
				slog.Any("error", err))
		}
	}()
}
