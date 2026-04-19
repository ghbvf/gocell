// Package configcore implements the config-core Cell: configuration management
// with versioning, publishing, rollback, and feature flag evaluation.
package configcore

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	cellpg "github.com/ghbvf/gocell/cells/config-core/internal/adapters/postgres"
	"github.com/ghbvf/gocell/cells/config-core/internal/mem"
	"github.com/ghbvf/gocell/cells/config-core/internal/ports"
	"github.com/ghbvf/gocell/cells/config-core/slices/configpublish"
	"github.com/ghbvf/gocell/cells/config-core/slices/configread"
	"github.com/ghbvf/gocell/cells/config-core/slices/configsubscribe"
	"github.com/ghbvf/gocell/cells/config-core/slices/configwrite"
	"github.com/ghbvf/gocell/cells/config-core/slices/featureflag"
	"github.com/ghbvf/gocell/cells/config-core/slices/flagwrite"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Compile-time interface checks.
var (
	_ cell.Cell           = (*ConfigCore)(nil)
	_ cell.HTTPRegistrar  = (*ConfigCore)(nil)
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

// WithPostgresDefaults wires the config-core cell with PostgreSQL-backed
// repositories and a transactional outbox. Use this option when
// GOCELL_CELL_ADAPTER_MODE=postgres. The caller is responsible for applying
// all migrations up to the current ExpectedVersion before starting; the
// adapterpg schema guard (VerifyExpectedVersion) enforces this at startup.
// Tables owned by config-core: config_entries / config_versions (004) and
// feature_flags (008). See adapters/postgres/migrations/ for the full set.
//
// pool must be a live pgxpool.Pool; outboxWriter is the outbox.Writer that
// writes to the outbox_entries table within the same transaction.
//
// L2 consistency: repo writes + outbox writes are wrapped in a single
// RunInTx call per service operation.
//
// ref: go-zero wire — adapter selected at assembly init time, not run time.
func WithPostgresDefaults(pool *pgxpool.Pool, outboxWriter outbox.Writer) Option {
	return func(c *ConfigCore) {
		session := cellpg.NewSession(pool)
		c.configRepo = cellpg.NewConfigRepository(session)
		c.flagRepo = cellpg.NewFlagRepository(session)
		c.outboxWriter = outboxWriter
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
	flagWriteHandler *flagwrite.Handler
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
	if err := c.validateOutboxDeps(deps); err != nil {
		return err
	}
	if err := c.ensureCursorCodec(deps); err != nil {
		return err
	}

	runMode := query.RunModeForDemo(deps.DurabilityMode == cell.DurabilityDemo)
	c.initWriteSlice()
	if err := c.initReadSlice(runMode); err != nil {
		return err
	}
	c.initPublishSlice()
	c.initSubscribeSlice()
	if err := c.initFlagSlice(runMode); err != nil {
		return err
	}
	c.initFlagWriteSlice()
	return nil
}

// validateOutboxDeps enforces the XOR constraint (outboxWriter + txRunner
// must be both present or both absent) and rejects noop implementations in
// durable mode.
func (c *ConfigCore) validateOutboxDeps(deps cell.Dependencies) error {
	// XOR constraint: both present = durable mode; both absent = demo mode.
	if (c.outboxWriter == nil) != (c.txRunner == nil) {
		return errcode.New(errcode.ErrCellMissingOutbox,
			"config-core durable mode requires both outboxWriter and txRunner")
	}
	if err := cell.CheckNotNoop(deps.DurabilityMode, "config-core", c.outboxWriter, c.txRunner, c.publisher); err != nil {
		return err
	}
	// Demo mode: require publisher for degraded event delivery.
	if c.outboxWriter == nil && c.txRunner == nil {
		if c.publisher == nil {
			return errcode.New(errcode.ErrCellMissingOutbox,
				"config-core requires publisher or outbox writer; use WithPublisher(&outbox.DiscardPublisher{}) for demo mode")
		}
		if c.ConsistencyLevel() >= cell.L2 {
			c.logger.Warn("config-core: running without outboxWriter+txRunner, L2 transactional atomicity not guaranteed (demo mode)",
				slog.String("cell", c.ID()),
				slog.Int("consistency_level", int(c.ConsistencyLevel())))
		}
	}
	return nil
}

// ensureCursorCodec sets a default cursor codec in demo mode or returns an
// error in durable mode when no codec was injected.
// ref: zeromicro/go-zero MustSetUp — fatal on insecure default config.
func (c *ConfigCore) ensureCursorCodec(deps cell.Dependencies) error {
	if c.cursorCodec != nil {
		return nil
	}
	if deps.DurabilityMode == cell.DurabilityDurable {
		return errcode.New(errcode.ErrCellMissingCodec,
			"config-core durable mode requires a cursor codec; use WithCursorCodec(query.NewCursorCodec(secret)) — the built-in demo key is public in the source tree")
	}
	// Each cell uses a distinct demo key to prevent cross-cell cursor reuse.
	codec, err := query.NewCursorCodec([]byte("gocell-demo-CONFIG-CORE-key-32!!"))
	if err != nil {
		return err
	}
	c.cursorCodec = codec
	c.logger.Warn("config-core: using default cursor codec (demo mode)",
		slog.String("cell", c.ID()))
	return nil
}

func (c *ConfigCore) initWriteSlice() {
	var opts []configwrite.Option
	if c.outboxWriter != nil {
		opts = append(opts, configwrite.WithOutboxWriter(c.outboxWriter))
	}
	if c.txRunner != nil {
		opts = append(opts, configwrite.WithTxManager(c.txRunner))
	}
	writeSvc := configwrite.NewService(c.configRepo, c.publisher, c.logger, opts...)
	c.writeHandler = configwrite.NewHandler(writeSvc)
	c.AddSlice(cell.NewBaseSlice("config-write", "config-core", cell.L2))
}

func (c *ConfigCore) initReadSlice(runMode query.RunMode) error {
	readSvc, err := configread.NewService(c.configRepo, c.cursorCodec, c.logger, runMode)
	if err != nil {
		return fmt.Errorf("config-read: %w", err)
	}
	c.readHandler = configread.NewHandler(readSvc)
	c.AddSlice(cell.NewBaseSlice("config-read", "config-core", cell.L0))
	return nil
}

func (c *ConfigCore) initPublishSlice() {
	var opts []configpublish.Option
	if c.outboxWriter != nil {
		opts = append(opts, configpublish.WithOutboxWriter(c.outboxWriter))
	}
	if c.txRunner != nil {
		opts = append(opts, configpublish.WithTxManager(c.txRunner))
	}
	publishSvc := configpublish.NewService(c.configRepo, c.publisher, c.logger, opts...)
	c.publishHandler = configpublish.NewHandler(publishSvc)
	c.AddSlice(cell.NewBaseSlice("config-publish", "config-core", cell.L2))
}

func (c *ConfigCore) initSubscribeSlice() {
	c.subscribeSvc = configsubscribe.NewService(c.logger)
	c.AddSlice(cell.NewBaseSlice("config-subscribe", "config-core", cell.L3))
}

func (c *ConfigCore) initFlagSlice(runMode query.RunMode) error {
	flagSvc, err := featureflag.NewService(c.flagRepo, c.cursorCodec, c.logger, runMode)
	if err != nil {
		return fmt.Errorf("feature-flag: %w", err)
	}
	c.flagHandler = featureflag.NewHandler(flagSvc)
	c.AddSlice(cell.NewBaseSlice("feature-flag", "config-core", cell.L0))
	return nil
}

// initFlagWriteSlice registers the flag-write L2 slice: Create/Update/Toggle/Delete
// with transactional outbox (flag.changed.v1 event).
func (c *ConfigCore) initFlagWriteSlice() {
	var opts []flagwrite.Option
	if c.outboxWriter != nil {
		opts = append(opts, flagwrite.WithOutboxWriter(c.outboxWriter))
	}
	if c.txRunner != nil {
		opts = append(opts, flagwrite.WithTxManager(c.txRunner))
	}
	flagWriteSvc := flagwrite.NewService(c.flagRepo, c.logger, opts...)
	c.flagWriteHandler = flagwrite.NewHandler(flagWriteSvc)
	c.AddSlice(cell.NewBaseSlice("flag-write", "config-core", cell.L2))
}

// RegisterRoutes registers HTTP routes for config-core. All admin-guarded
// write handlers delegate to the slice's RegisterRoutes (which applies
// auth.Secured + auth.AnyRole(RoleAdmin)) so the policy declaration cannot
// drift between production wiring and contract/integration tests — there is
// exactly one place where each write endpoint's policy is declared.
//
// ref: kubernetes/kubernetes pkg/endpoints/installer.go — one installer per
// resource, authz chain applied once at registration.
// ref: go-kratos/kratos transport/http/server.go — route + middleware pair
// declared once; runtime and test paths share the same registration call.
func (c *ConfigCore) RegisterRoutes(mux cell.RouteMux) {
	mux.Route("/api/v1", func(v1 cell.RouteMux) {
		// Config CRUD + publish/rollback under /api/v1/config.
		v1.Route("/config", func(cfg cell.RouteMux) {
			// config-read (unauthenticated GETs by design).
			cfg.Handle("GET /", http.HandlerFunc(c.readHandler.HandleList))
			cfg.Handle("GET /{key}", http.HandlerFunc(c.readHandler.HandleGet))
			// config-write — admin-guarded via slice RegisterRoutes.
			c.writeHandler.RegisterRoutes(cfg)
			// config-publish — admin-guarded via slice RegisterRoutes.
			c.publishHandler.RegisterRoutes(cfg)
		})

		// /api/v1/flags hosts feature-flag (read + evaluate, L0) and flag-write
		// (write + toggle + delete, L2 + admin-guarded).
		v1.Route("/flags", func(f cell.RouteMux) {
			// feature-flag (read) slice — unauthenticated.
			f.Handle("GET /", http.HandlerFunc(c.flagHandler.HandleList))
			f.Handle("GET /{key}", http.HandlerFunc(c.flagHandler.HandleGet))
			f.Handle("POST /{key}/evaluate", http.HandlerFunc(c.flagHandler.HandleEvaluate))
			// flag-write — admin-guarded via slice RegisterRoutes.
			c.flagWriteHandler.RegisterRoutes(f)
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
