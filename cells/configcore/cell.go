// Package configcore implements the configcore Cell: configuration management
// with versioning, publishing, rollback, and feature flag evaluation.
package configcore

import (
	"context"
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
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/runtime/crypto"
	"github.com/jackc/pgx/v5/pgxpool"
	prom "github.com/prometheus/client_golang/prometheus"
)

// Compile-time interface checks.
var (
	_ cell.Cell                  = (*ConfigCore)(nil)
	_ cell.RouteGroupContributor = (*ConfigCore)(nil)
	_ cell.EventRegistrar        = (*ConfigCore)(nil)
	_ cell.HealthContributor     = (*ConfigCore)(nil)
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
	return func(c *ConfigCore) { c.emitter = e }
}

// WithOutboxDeps wires raw outbox dependencies (Publisher + Writer). The
// framework composes them into an outbox.Emitter at Init() time via
// cell.ResolveEmitter.
//
// Accumulative: a nil argument leaves the previously-set value in place;
// multiple calls combine their non-nil arguments. Does NOT clear previous
// state — `WithOutboxDeps(nil, nil)` is a no-op, not a reset. Mutually
// exclusive with WithEmitter; Init() fails fast if both are set.
func WithOutboxDeps(pub outbox.Publisher, writer outbox.Writer) Option {
	return func(c *ConfigCore) {
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
	return func(c *ConfigCore) { c.logger = l }
}

// WithTxManager sets the TxRunner for transactional guarantees (L2 atomicity).
func WithTxManager(tx persistence.TxRunner) Option {
	return func(c *ConfigCore) { c.txRunner = tx }
}

// WithMetricsProvider sets the metrics provider used by the DirectEmitter in
// demo mode. Required when WithOutboxDeps sets a publisher without a real
// outboxWriter. Pass metrics.NopProvider{} explicitly in tests.
func WithMetricsProvider(p metrics.Provider) Option {
	return func(c *ConfigCore) { c.metricsProvider = p }
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

// WithPostgresPool wires configcore with a PostgreSQL-backed pool for its
// own repositories and the shared transactional outbox. Use when
// GOCELL_CELL_ADAPTER_MODE=postgres. The caller is responsible for applying
// all migrations up to the current ExpectedVersion before starting; the
// adapterpg schema guard (VerifyExpectedVersion) enforces this at startup.
// Tables owned by configcore: config_entries / config_versions (004) and
// feature_flags (008 table + 009 concurrent index).
//
// Outbox wiring is the caller's responsibility — call WithOutboxDeps(pub,
// writer) separately (the writer shares the pool via the per-cell adapter).
//
// Encryption: when a KeyProvider or ValueTransformer has been configured via
// WithKeyProvider/WithValueTransformer, the PG repo encrypts sensitive=true
// values at the repository boundary. The transformer is resolved at Init() so
// WithKeyProvider and WithPostgresPool may be called in any order.
//
// L2 consistency: repo writes + outbox writes are wrapped in a single
// RunInTx call per service operation.
//
// ref: go-zero wire — adapter selected at assembly init time, not run time.
func WithPostgresPool(pool *pgxpool.Pool) Option {
	return func(c *ConfigCore) {
		// Store the pool for deferred repo construction in Init().
		c.pgPool = pool
	}
}

// ConfigCore is the configcore Cell implementation.
type ConfigCore struct {
	*cell.BaseCell
	configRepo ports.ConfigRepository
	flagRepo   ports.FlagRepository

	// Outbox wiring (see WithEmitter / WithOutboxDeps godoc for the two
	// mutually exclusive paths). Private by construction; no exported Option
	// takes raw outbox.Publisher/Writer arguments (archtest OUTBOX-CELL-01).
	emitter             outbox.Emitter
	pendingOutboxPub    outbox.Publisher
	pendingOutboxWriter outbox.Writer

	txRunner           persistence.TxRunner
	cursorCodec        *query.CursorCodec
	logger             *slog.Logger
	metricsProvider    metrics.Provider
	keyProvider        kcrypto.KeyProvider
	valueTransformer   kcrypto.ValueTransformer
	pgPool             *pgxpool.Pool // stored by WithPostgresPool for deferred Init()
	staleCipherCounter prom.Counter  // optional; incremented on stale-key reads (M3)

	// Slice services and handlers.
	writeHandler     *configwrite.Handler
	readHandler      *configread.Handler
	publishHandler   *configpublish.Handler
	flagHandler      *featureflag.Handler
	flagWriteHandler *flagwrite.Handler
	subscribeSvc     *configsubscribe.Service
}

// HealthCheckers implements cell.HealthContributor. Aggregates the outbox
// emitter's HealthCheckers (currently fail-open drop rate → degraded signal)
// so /readyz surfaces "config events are being lost in fail-open path"
// without polluting the cell's primary Cell.Health() signal.
func (c *ConfigCore) HealthCheckers() map[string]func(context.Context) error {
	checkers := make(map[string]func(context.Context) error)
	if hc, ok := c.emitter.(cell.HealthContributor); ok {
		for k, v := range hc.HealthCheckers() {
			checkers[k] = v
		}
	}
	return checkers
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
