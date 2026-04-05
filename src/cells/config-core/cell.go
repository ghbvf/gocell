// Package configcore implements the config-core Cell: configuration management
// with versioning, publishing, rollback, and feature flag evaluation.
package configcore

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

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
	// All config routes under /api/v1/config using a single router.
	configRouter := chi.NewRouter()
	configRouter.Route("/", func(r chi.Router) {
		// config-read
		r.Get("/", wrapRouter(c.readHandler.Routes()))
		r.Get("/{key}", wrapRouter(c.readHandler.Routes()))
		// config-write
		r.Post("/", wrapRouter(c.writeHandler.Routes()))
		r.Put("/{key}", wrapRouter(c.writeHandler.Routes()))
		r.Delete("/{key}", wrapRouter(c.writeHandler.Routes()))
		// config-publish
		r.Post("/{key}/publish", wrapRouter(c.publishHandler.Routes()))
		r.Post("/{key}/rollback", wrapRouter(c.publishHandler.Routes()))
	})
	mux.Handle("/api/v1/config/*", configRouter)

	// feature-flag: /api/v1/flags
	mux.Handle("/api/v1/flags/*", c.flagHandler.Routes())
}

// wrapRouter converts a chi.Router to an http.HandlerFunc for inline mounting.
func wrapRouter(r chi.Router) func(w http.ResponseWriter, req *http.Request) {
	return r.ServeHTTP
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
