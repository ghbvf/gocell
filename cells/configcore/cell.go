// Package configcore implements the configcore Cell: configuration management
// with versioning, publishing, rollback, and feature flag evaluation.
package configcore

import (
	"log/slog"

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
	"github.com/ghbvf/gocell/pkg/query"
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
