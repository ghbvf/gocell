// Package configcore implements the configcore Cell: configuration management
// with versioning, publishing, rollback, and feature flag evaluation.
package configcore

import (
	"log/slog"
	"time"

	"github.com/ghbvf/gocell/cells/configcore/internal/ports"
	"github.com/ghbvf/gocell/cells/configcore/slices/configpublish"
	"github.com/ghbvf/gocell/cells/configcore/slices/configread"
	"github.com/ghbvf/gocell/cells/configcore/slices/configsubscribe"
	"github.com/ghbvf/gocell/cells/configcore/slices/configwrite"
	"github.com/ghbvf/gocell/cells/configcore/slices/featureflag"
	"github.com/ghbvf/gocell/cells/configcore/slices/flagwrite"
	"github.com/ghbvf/gocell/kernel/cell"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/query"
	"github.com/ghbvf/gocell/pkg/validation"
	obmetrics "github.com/ghbvf/gocell/runtime/observability/metrics"
	"github.com/ghbvf/gocell/runtime/state/cas"
)

// VersionField is the DB column name used as the CAS version field for
// optimistic-concurrency control on config entries and feature flags.
// Composition root uses this constant when wiring cas.Protocol:
//
//	cas.MustNewProtocol(cas.WithVersionField(configcore.VersionField))
const VersionField = "version"

// Compile-time interface check lives in cell_gen.go (DO NOT EDIT).

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

// WithOutboxDeps wires sealed outbox dependencies (CellPublisher +
// CellWriter). Composition roots construct each via
// outbox.WrapPublisherForCell / outbox.WrapWriterForCell. The framework
// composes them into an outbox.Emitter at Init() time via
// cell.ResolveCellEmitter.
//
// Accumulative: a nil argument leaves the previously-set value in place;
// multiple calls combine their non-nil arguments. Does NOT clear previous
// state — `WithOutboxDeps(nil, nil)` is a no-op, not a reset. Mutually
// exclusive with WithEmitter; Init() fails fast if both are set.
//
// AI-HARD per ADR cell-raw-infra-sealed-marker: the option signature
// rejects raw outbox.Publisher / outbox.Writer at compile time.
func WithOutboxDeps(pub outbox.CellPublisher, writer outbox.CellWriter) Option {
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

// WithTxManager sets the CellTxManager for transactional guarantees
// (L2 atomicity). Composition roots construct via persistence.WrapForCell.
func WithTxManager(tx persistence.CellTxManager) Option {
	return func(c *ConfigCore) { c.txRunner = tx }
}

// WithMetricsProvider sets the metrics provider used by the DirectEmitter in
// demo mode. Required when WithOutboxDeps sets a publisher without a real
// outboxWriter. Pass metrics.NopProvider{} explicitly in tests.
func WithMetricsProvider(p metrics.Provider) Option {
	return func(c *ConfigCore) { c.metricsProvider = p }
}

// WithConfigEventCollector injects config-event consumer process metrics.
func WithConfigEventCollector(collector obmetrics.ConfigEventCollector) Option {
	return func(c *ConfigCore) { c.configEventCollector = collector }
}

// WithTombstoneTTL sets the configsubscribe cache tombstone TTL. The cell
// forwards it to the configsubscribe service, which applies a 24h default
// when unset (must stay >= the Claimer idempotency window — see
// configsubscribe.Service godoc). Passing 0 or a negative duration results in
// the 24h default (>= Claimer idempotency window); there is no API to disable
// the tombstone-GC via this option.
func WithTombstoneTTL(d time.Duration) Option { return func(c *ConfigCore) { c.tombstoneTTL = d } }

// WithEventbusCacheCollector injects the subscriber-cache tombstone-GC metric
// collector. nil is normalized to NoopEventbusCacheCollector by the slice.
func WithEventbusCacheCollector(col obmetrics.EventbusCacheCollector) Option {
	return func(c *ConfigCore) { c.cacheCollector = col }
}

// WithCursorCodec sets the cursor codec for pagination.
func WithCursorCodec(codec *query.CursorCodec) Option {
	return func(c *ConfigCore) { c.cursorCodec = codec }
}

// WithClock sets the time source for this Cell. Required — Init() panics via
// clock.MustHaveClock if not set. Composition root passes clock.Real(); tests
// inject a deterministic clock to control time-sensitive logic.
func WithClock(clk clock.Clock) Option {
	return func(c *ConfigCore) { c.clk = clk }
}

// WithCASProtocol sets the CAS protocol declaration for this Cell. Required in
// durable mode — initInternal() fails fast if nil. Both bare-nil and typed-nil
// *cas.Protocol values are rejected via sticky sentinel. Composition root
// constructs via cas.MustNewProtocol (CAS-PROTOCOL-COMPOSITION-ROOT-01);
// tests may inject a test-scoped *cas.Protocol.
//
// ref: runtime/http/router WithRateLimiter — same strong-dependency wiring
// option pattern (option body sets sticky nil sentinel; phase0 validates).
func WithCASProtocol(p *cas.Protocol) Option {
	return func(c *ConfigCore) {
		if validation.IsNilInterface(p) {
			c.casProtocolNil = true
			return
		}
		c.casProtocol = p
	}
}

// WithInMemoryDefaults configures in-memory repositories for development
// and testing. Not suitable for production use.
// Repository construction is deferred to Init() so that c.clk is available
// when mem.NewConfigRepository/NewFlagRepository are called.
func WithInMemoryDefaults() Option {
	return func(c *ConfigCore) { c.useInMemoryDefaults = true }
}

// ConfigCore is the configcore Cell implementation.
// +cell:listener:ref=cell.PrimaryListener,prefix=/api/v1
// +cell:listener:ref=cell.InternalListener,prefix=/internal/v1
type ConfigCore struct {
	*cell.BaseCell
	clk        clock.Clock
	configRepo ports.ConfigRepository
	flagRepo   ports.FlagRepository

	// useInMemoryDefaults tracks whether WithInMemoryDefaults was applied so
	// Init() can construct mem repos after c.clk is set from deps.Clock.
	useInMemoryDefaults bool

	// casProtocol is the CAS protocol declaration for this Cell.
	// Set by WithCASProtocol; validated non-nil in initInternal.
	// casProtocolNil is a sticky sentinel: set when WithCASProtocol receives a
	// typed-nil *cas.Protocol (both bare-nil and typed-nil are rejected).
	casProtocol    *cas.Protocol
	casProtocolNil bool

	// Outbox wiring (see WithEmitter / WithOutboxDeps godoc for the two
	// mutually exclusive paths). Sealed marker types prevent any cell.go
	// public Option from accepting raw outbox.Publisher / outbox.Writer at
	// compile time (ADR cell-raw-infra-sealed-marker §D1).
	emitter             outbox.Emitter
	pendingOutboxPub    outbox.CellPublisher
	pendingOutboxWriter outbox.CellWriter

	txRunner             persistence.CellTxManager
	cursorCodec          *query.CursorCodec
	logger               *slog.Logger
	metricsProvider      metrics.Provider
	configEventCollector obmetrics.ConfigEventCollector
	tombstoneTTL         time.Duration
	cacheCollector       obmetrics.EventbusCacheCollector

	// Slice services and handlers.
	// +slice:route:slice=configwrite,subPath=/config
	writeHandler *configwrite.Handler

	// +slice:route:slice=configread,subPath=/config
	// +slice:route:slice=configread,listener=cell.InternalListener,subPath=/config,method=RegisterInternalRoutes
	readHandler *configread.Handler

	// +slice:route:slice=configpublish,subPath=/config
	publishHandler *configpublish.Handler

	// +slice:route:slice=featureflag,subPath=/flags
	flagHandler *featureflag.Handler

	// +slice:route:slice=flagwrite,subPath=/flags
	flagWriteHandler *flagwrite.Handler

	// +slice:subscribe:slice=configsubscribe,topic=event.config.entry-upserted.v1,handler=HandleEntryUpserted,group=configcore
	// +slice:subscribe:slice=configsubscribe,topic=event.config.entry-deleted.v1,handler=HandleEntryDeleted,group=configcore
	subscribeSvc *configsubscribe.Service
}

// NewConfigCore creates a new ConfigCore Cell.
func NewConfigCore(opts ...Option) *ConfigCore {
	c := &ConfigCore{
		BaseCell: cell.MustNewBaseCell(loadCellMetadata()),
		logger:   slog.Default(),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}
