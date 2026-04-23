// Package configcore implements the configcore Cell: configuration management
// with versioning, publishing, rollback, and feature flag evaluation.
package configcore

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"

	cellpg "github.com/ghbvf/gocell/cells/configcore/internal/adapters/postgres"
	"github.com/ghbvf/gocell/cells/configcore/internal/mem"
	"github.com/ghbvf/gocell/cells/configcore/internal/ports"
	"github.com/ghbvf/gocell/cells/configcore/slices/configpublish"
	"github.com/ghbvf/gocell/cells/configcore/slices/configread"
	"github.com/ghbvf/gocell/cells/configcore/slices/configsubscribe"
	"github.com/ghbvf/gocell/cells/configcore/slices/configwrite"
	"github.com/ghbvf/gocell/cells/configcore/slices/featureflag"
	"github.com/ghbvf/gocell/cells/configcore/slices/flagwrite"
	"github.com/ghbvf/gocell/kernel/cell"
	kcrypto "github.com/ghbvf/gocell/kernel/crypto"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/auth"
	"github.com/ghbvf/gocell/runtime/crypto"
	"github.com/jackc/pgx/v5/pgxpool"
	prom "github.com/prometheus/client_golang/prometheus"
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

// WithKeyProvider sets the KeyProvider for sensitive config value encryption.
// The provider is used to construct a ValueTransformer that encrypts/decrypts
// sensitive=true values at the repository boundary.
func WithKeyProvider(p kcrypto.KeyProvider) Option {
	return func(c *ConfigCore) {
		c.keyProvider = p
		c.valueTransformer = crypto.NewValueTransformer(p)
	}
}

// WithValueTransformer sets the ValueTransformer directly (alternative to
// WithKeyProvider; useful in tests that inject a pre-built transformer).
func WithValueTransformer(t kcrypto.ValueTransformer) Option {
	return func(c *ConfigCore) { c.valueTransformer = t }
}

// WithOnStaleCipherMetric sets a Prometheus counter that increments when a
// config value encrypted with a stale (non-current) key is read. Wire this
// from the composition root to enable M3 stale-key observability.
func WithOnStaleCipherMetric(c prom.Counter) Option {
	return func(cc *ConfigCore) { cc.staleCipherCounter = c }
}

// WithPostgresDefaults wires the configcore cell with PostgreSQL-backed
// repositories and a transactional outbox. Use this option when
// GOCELL_CELL_ADAPTER_MODE=postgres. The caller is responsible for applying
// all migrations up to the current ExpectedVersion before starting; the
// adapterpg schema guard (VerifyExpectedVersion) enforces this at startup.
// Tables owned by configcore: config_entries / config_versions (004) and
// feature_flags (008 table + 009 concurrent index). See
// adapters/postgres/migrations/ for the full set.
//
// pool must be a live pgxpool.Pool; outboxWriter is the outbox.Writer that
// writes to the outbox_entries table within the same transaction.
//
// Encryption: when a KeyProvider or ValueTransformer has been configured via
// WithKeyProvider/WithValueTransformer, the PG repo encrypts sensitive=true
// values at the repository boundary. The transformer is resolved at Init() so
// WithKeyProvider and WithPostgresDefaults may be called in any order.
//
// L2 consistency: repo writes + outbox writes are wrapped in a single
// RunInTx call per service operation.
//
// ref: go-zero wire — adapter selected at assembly init time, not run time.
func WithPostgresDefaults(pool *pgxpool.Pool, outboxWriter outbox.Writer) Option {
	return func(c *ConfigCore) {
		// Store the pool for deferred repo construction in Init().
		c.pgPool = pool
		c.outboxWriter = outboxWriter
	}
}

// ConfigCore is the configcore Cell implementation.
type ConfigCore struct {
	*cell.BaseCell
	configRepo         ports.ConfigRepository
	flagRepo           ports.FlagRepository
	publisher          outbox.Publisher
	outboxWriter       outbox.Writer
	txRunner           persistence.TxRunner
	emitter            outbox.Emitter
	cursorCodec        *query.CursorCodec
	logger             *slog.Logger
	keyProvider        kcrypto.KeyProvider
	valueTransformer   kcrypto.ValueTransformer
	pgPool             *pgxpool.Pool // stored by WithPostgresDefaults for deferred Init()
	staleCipherCounter prom.Counter  // optional; incremented on stale-key reads (M3)

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
			ID:               "configcore",
			Type:             cell.CellTypeCore,
			ConsistencyLevel: cell.L2,
			Owner:            cell.Owner{Team: "platform", Role: "config-owner"},
			Schema:           cell.SchemaConfig{Primary: "config_entries"},
			Verify:           cell.CellVerify{Smoke: []string{"configcore/smoke"}},
		}),
		logger: slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Init constructs all slices and registers them. If pgPool is set
// (via WithPostgresDefaults), the PG repos are built here after all options
// have been applied (so WithKeyProvider can precede or follow WithPostgresDefaults).
func (c *ConfigCore) Init(ctx context.Context, deps cell.Dependencies) error {
	if err := c.BaseCell.Init(ctx, deps); err != nil {
		return err
	}

	// Deferred PG repo construction — options are all applied before Init().
	if c.pgPool != nil && c.configRepo == nil {
		session := cellpg.NewSession(c.pgPool)
		var repoOpts []cellpg.ConfigRepoOption
		if c.staleCipherCounter != nil {
			repoOpts = append(repoOpts, cellpg.WithOnStaleCipher(func(_, _, _ string) {
				c.staleCipherCounter.Inc()
			}))
		}
		c.configRepo = cellpg.NewConfigRepository(session, c.valueTransformer, nil, repoOpts...)
		c.flagRepo = cellpg.NewFlagRepository(session)
	}

	runMode, publishFailureMode := c.deriveModes(deps.DurabilityMode)

	if err := c.validateOutboxDeps(deps, publishFailureMode); err != nil {
		return err
	}
	if err := c.ensureCursorCodec(deps); err != nil {
		return err
	}

	c.initWriteSlice()
	if err := c.initReadSlice(runMode); err != nil {
		return err
	}
	c.initPublishSlice(publishFailureMode)
	c.initSubscribeSlice()
	if err := c.initFlagSlice(runMode); err != nil {
		return err
	}
	if err := c.initFlagWriteSlice(); err != nil {
		return err
	}
	return nil
}

// deriveModes is the single translation point from kernel/cell.DurabilityMode
// to run modes used by slices. Called only once at Init() time; propagated via
// constructor parameters (do not call in handler/repository).
//
// S10 MODE-SEMANTIC-SPLIT-01: read-path cursor tolerance (RunMode) and write-
// path publisher failure semantics (PublishFailureMode) are separate types that
// evolve independently.
//
// ref: Uber fx Provide/Decorate — each decision gets its own typed injection.
func (c *ConfigCore) deriveModes(durabilityMode cell.DurabilityMode) (query.RunMode, configpublish.PublishFailureMode) {
	demo := durabilityMode == cell.DurabilityDemo
	return query.RunModeForDemo(demo), configpublish.PublishFailureModeForDemo(demo)
}

// validateOutboxDeps enforces the XOR constraint (outboxWriter + txRunner
// must be both present or both absent) and rejects noop implementations in
// durable mode.
func (c *ConfigCore) validateOutboxDeps(deps cell.Dependencies, publishFailureMode configpublish.PublishFailureMode) error {
	if err := cell.CheckNotNoop(deps.DurabilityMode, "configcore", c.outboxWriter, c.txRunner, c.publisher); err != nil {
		return err
	}
	if deps.DurabilityMode == cell.DurabilityDurable {
		if c.outboxWriter == nil || c.txRunner == nil {
			return errcode.New(errcode.ErrCellMissingOutbox,
				"configcore durable mode requires real outboxWriter and txRunner")
		}
		emitter, err := outbox.NewWriterEmitter(c.outboxWriter)
		if err != nil {
			return err
		}
		c.emitter = emitter
		return nil
	}

	c.txRunner = persistence.RunnerOrNoop(c.txRunner)
	emitter, err := c.resolveDemoEmitter(publishFailureMode)
	if err != nil {
		return err
	}
	c.emitter = emitter
	if c.ConsistencyLevel() >= cell.L2 && c.outboxWriter == nil {
		c.logger.Warn("configcore: running without outboxWriter+txRunner, L2 transactional atomicity not guaranteed (demo mode)",
			slog.String("cell", c.ID()),
			slog.Int("consistency_level", int(c.ConsistencyLevel())))
	}
	return nil
}

func (c *ConfigCore) resolveDemoEmitter(publishFailureMode configpublish.PublishFailureMode) (outbox.Emitter, error) {
	if c.publisher != nil && (c.outboxWriter == nil || isNoopDep(c.outboxWriter)) {
		return outbox.NewDirectEmitter(c.publisher, configDirectPublishMode(publishFailureMode), c.logger)
	}
	if c.outboxWriter != nil {
		return outbox.NewWriterEmitter(c.outboxWriter)
	}
	return outbox.NewNoopEmitter(), nil
}

func configDirectPublishMode(mode configpublish.PublishFailureMode) outbox.DirectPublishFailureMode {
	if mode.IsFailOpen() {
		return outbox.DirectPublishFailOpen
	}
	return outbox.DirectPublishFailClosed
}

func isNoopDep(dep any) bool {
	n, ok := dep.(interface{ Noop() bool })
	return ok && n.Noop()
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
			"configcore durable mode requires a cursor codec; use WithCursorCodec(query.NewCursorCodec(secret)) — the built-in demo key is public in the source tree")
	}
	// Each cell uses a distinct demo key to prevent cross-cell cursor reuse.
	codec, err := query.NewCursorCodec([]byte("gocell-demo-CONFIG-CORE-key-32!!"))
	if err != nil {
		return err
	}
	c.cursorCodec = codec
	c.logger.Warn("configcore: using default cursor codec (demo mode)",
		slog.String("cell", c.ID()))
	return nil
}

func (c *ConfigCore) initWriteSlice() {
	opts := []configwrite.Option{configwrite.WithEmitter(c.emitter), configwrite.WithTxManager(c.txRunner)}
	writeSvc := configwrite.NewService(c.configRepo, c.logger, opts...)
	c.writeHandler = configwrite.NewHandler(writeSvc)
	c.AddSlice(cell.NewBaseSlice("configwrite", "configcore", cell.L2))
}

func (c *ConfigCore) initReadSlice(runMode query.RunMode) error {
	readSvc, err := configread.NewService(c.configRepo, c.cursorCodec, c.logger, runMode)
	if err != nil {
		return fmt.Errorf("config-read: %w", err)
	}
	c.readHandler = configread.NewHandler(readSvc)
	c.AddSlice(cell.NewBaseSlice("configread", "configcore", cell.L0))
	return nil
}

func (c *ConfigCore) initPublishSlice(publishFailureMode configpublish.PublishFailureMode) {
	opts := []configpublish.Option{
		configpublish.WithEmitter(c.emitter),
		configpublish.WithTxManager(c.txRunner),
		configpublish.WithPublishFailureMode(publishFailureMode),
	}
	publishSvc := configpublish.NewService(c.configRepo, c.logger, opts...)
	c.publishHandler = configpublish.NewHandler(publishSvc)
	c.AddSlice(cell.NewBaseSlice("configpublish", "configcore", cell.L2))
}

func (c *ConfigCore) initSubscribeSlice() {
	c.subscribeSvc = configsubscribe.NewService(c.logger)
	c.AddSlice(cell.NewBaseSlice("configsubscribe", "configcore", cell.L3))
}

func (c *ConfigCore) initFlagSlice(runMode query.RunMode) error {
	flagSvc, err := featureflag.NewService(c.flagRepo, c.cursorCodec, c.logger, runMode)
	if err != nil {
		return fmt.Errorf("feature-flag: %w", err)
	}
	c.flagHandler = featureflag.NewHandler(flagSvc)
	c.AddSlice(cell.NewBaseSlice("featureflag", "configcore", cell.L0))
	return nil
}

// initFlagWriteSlice registers the flag-write L2 slice: Create/Update/Toggle/Delete
// with transactional outbox (flag.changed.v1 event).
func (c *ConfigCore) initFlagWriteSlice() error {
	opts := []flagwrite.Option{flagwrite.WithEmitter(c.emitter), flagwrite.WithTxManager(c.txRunner)}
	flagWriteSvc, err := flagwrite.NewService(c.flagRepo, c.logger, opts...)
	if err != nil {
		return fmt.Errorf("configcore: init flag-write slice: %w", err)
	}
	c.flagWriteHandler = flagwrite.NewHandler(flagWriteSvc)
	c.AddSlice(cell.NewBaseSlice("flagwrite", "configcore", cell.L2))
	return nil
}

// RegisterRoutes registers HTTP routes for configcore. All admin-guarded
// write handlers delegate to the slice's RegisterRoutes (which applies
// auth.Declare + auth.AnyRole(RoleAdmin)) so the policy declaration cannot
// drift between production wiring and contract/integration tests — there is
// exactly one place where each write endpoint's policy is declared.
//
// Read endpoints (GET /config, GET /config/{key}, GET /flags, GET /flags/{key},
// POST /flags/{key}/evaluate) are declared with auth.Authenticated() to gain
// an explicit auth declaration; previously these relied on implicit authed-by-
// default behaviour.
//
// ref: kubernetes/kubernetes pkg/endpoints/installer.go — one installer per
// resource, authz chain applied once at registration.
// ref: go-kratos/kratos transport/http/server.go — route + middleware pair
// declared once; runtime and test paths share the same registration call.
func (c *ConfigCore) RegisterRoutes(mux cell.RouteMux) {
	mux.Route("/api/v1", func(v1 cell.RouteMux) {
		// Config CRUD + publish/rollback under /api/v1/config.
		v1.Route("/config", func(cfg cell.RouteMux) {
			// config-read — authenticated via auth.Declare.
			auth.Declare(cfg, auth.RouteDecl{
				Method:  "GET",
				Path:    "/",
				Handler: http.HandlerFunc(c.readHandler.HandleList),
				Policy:  auth.Authenticated(),
			})
			auth.Declare(cfg, auth.RouteDecl{
				Method:  "GET",
				Path:    "/{key}",
				Handler: http.HandlerFunc(c.readHandler.HandleGet),
				Policy:  auth.Authenticated(),
			})
			// config-write — admin-guarded via slice RegisterRoutes.
			c.writeHandler.RegisterRoutes(cfg)
			// config-publish — admin-guarded via slice RegisterRoutes.
			c.publishHandler.RegisterRoutes(cfg)
		})

		// /api/v1/flags hosts feature-flag (read + evaluate, L0) and flag-write
		// (write + toggle + delete, L2 + admin-guarded).
		v1.Route("/flags", func(f cell.RouteMux) {
			// feature-flag (read) slice — authenticated via auth.Declare.
			auth.Declare(f, auth.RouteDecl{
				Method:  "GET",
				Path:    "/",
				Handler: http.HandlerFunc(c.flagHandler.HandleList),
				Policy:  auth.Authenticated(),
			})
			auth.Declare(f, auth.RouteDecl{
				Method:  "GET",
				Path:    "/{key}",
				Handler: http.HandlerFunc(c.flagHandler.HandleGet),
				Policy:  auth.Authenticated(),
			})
			auth.Declare(f, auth.RouteDecl{
				Method:  "POST",
				Path:    "/{key}/evaluate",
				Handler: http.HandlerFunc(c.flagHandler.HandleEvaluate),
				Policy:  auth.Authenticated(),
			})
			// flag-write — admin-guarded via slice RegisterRoutes.
			c.flagWriteHandler.RegisterRoutes(f)
		})
	})
}

// RegisterSubscriptions declares event subscriptions for configcore.
// The Router manages goroutine lifecycle and setup-error detection.
func (c *ConfigCore) RegisterSubscriptions(r cell.EventRouter) error {
	handler := outbox.WrapLegacyHandler(c.subscribeSvc.HandleEvent)
	r.AddHandler(configsubscribe.TopicConfigChanged, handler, "configcore")
	return nil
}
