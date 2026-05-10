package outbox

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"

	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// Emitter emits an outbox entry either by writing it to a durable outbox or by
// directly publishing its canonical wire envelope.
//
// Implementations may optionally satisfy DurabilityReporter to expose whether
// their sink is backed by durable (transactional outbox) storage. Callers that
// need to decide L2/L0 slice upgrades should use ReportDurable, which returns
// false for any Emitter that does not implement the optional interface.
type Emitter interface {
	Emit(ctx context.Context, entry Entry) error
}

// DirectPublishFailureMode controls direct publisher error handling.
type DirectPublishFailureMode int

const (
	DirectPublishFailClosed DirectPublishFailureMode = iota + 1
	DirectPublishFailOpen
)

// WarnDirectPublishFailOpen is the slog.Warn message emitted when a
// DirectEmitter in DirectPublishFailOpen mode swallows a publisher failure.
// Tests assert on this constant to lock the observable signal — AI-rebust
// Medium (single-source literal, typed reference). Counter equivalent:
// outbox_emit_failopen_dropped_total (registered in NewDirectEmitter).
const WarnDirectPublishFailOpen = "outbox: direct publish failed (fail-open) — event dropped"

// WriterEmitter emits by writing entries to the transactional outbox.
type WriterEmitter struct {
	writer Writer
}

// NewWriterEmitter adapts an outbox Writer into an Emitter.
func NewWriterEmitter(w Writer) (*WriterEmitter, error) {
	if isNilEmitterDependency(w) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellMissingOutbox,
			"outbox: nil writer for WriterEmitter")
	}
	return &WriterEmitter{writer: w}, nil
}

// NewNoopEmitter returns an Emitter backed by NoopWriter.
func NewNoopEmitter() Emitter {
	return &WriterEmitter{writer: NoopWriter{}}
}

// Emit writes the entry to the underlying transactional outbox. The relay
// then publishes it asynchronously, so durability is decoupled from the
// caller's success path. Returns ErrCellMissingOutbox when the writer is
// nil — a programmer error that should surface at construction time.
func (e *WriterEmitter) Emit(ctx context.Context, entry Entry) error {
	if e == nil || isNilEmitterDependency(e.writer) {
		return errcode.New(errcode.KindInternal, errcode.ErrCellMissingOutbox,
			"outbox: nil writer for WriterEmitter")
	}
	return e.writer.Write(ctx, entry)
}

var _ Emitter = (*WriterEmitter)(nil)

// DirectEmitter emits by wrapping entries in the v1 wire envelope and calling
// Publisher.Publish directly.
type DirectEmitter struct {
	publisher         Publisher
	mode              DirectPublishFailureMode
	clock             clock.Clock
	cellID            string
	logger            *slog.Logger
	failOpenDroppedCv metrics.CounterVec
	failOpenTracker   *failOpenTracker
}

// DirectEmitterOption configures NewDirectEmitter.
type DirectEmitterOption func(*directEmitterOptions)

type directEmitterOptions struct {
	logger             *slog.Logger
	failOpenRateThresh float64
}

// WithLogger overrides slog.Default() for this DirectEmitter's structured logs.
func WithLogger(l *slog.Logger) DirectEmitterOption {
	return func(o *directEmitterOptions) { o.logger = l }
}

// WithFailOpenRateThreshold sets the drop ratio threshold above which the
// emitter's Probes checker reports cell.ErrDegraded.
//
// Default is 0.05 (5%) — the tracker is enabled by default because fail-open
// drop monitoring is framework infrastructure responsibility, not a per-cell
// opt-in (CLAUDE.md "生产配置禁止静默降级"). Pass WithFailOpenRateThreshold(0)
// to explicitly disable.
//
// The implicit time window is the interval between two /readyz probes
// (typically 10-30s under K8s readinessProbe).
//
// ref: kernel/outbox/failopen_tracker.go for ratio semantics.
func WithFailOpenRateThreshold(ratio float64) DirectEmitterOption {
	return func(o *directEmitterOptions) { o.failOpenRateThresh = ratio }
}

const defaultFailOpenRateThreshold = 0.05 // 5%

// NewDirectEmitter adapts a Publisher into an Emitter that publishes v1 wire
// envelopes. cellID is the owning Cell's ID and is used as the "cell" label on
// the fail-open dropped counter; it must be non-empty (empty string returns
// errcode.ErrValidationFailed). mp is a required metrics.Provider used to
// register the fail-open dropped counter (fqName after Namespace injection:
// gocell_outbox_emit_failopen_dropped_total); pass metrics.NopProvider{} in
// tests or demos where no backend is wired. A nil mp returns an errcode error.
// clk is the clock used to stamp entry.CreatedAt when the caller has not set
// it; pass clock.Real() in production and clockmock.New(...) in tests.
//
// Use WithLogger to override the default slog.Default() logger.
// Use WithFailOpenRateThreshold to set the drop-ratio threshold for the
// Probes checker (default 5%; 0 disables).
func NewDirectEmitter(
	p Publisher, mode DirectPublishFailureMode, mp metrics.Provider, clk clock.Clock, cellID string, opts ...DirectEmitterOption,
) (*DirectEmitter, error) {
	if isNilEmitterDependency(p) {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellMissingOutbox,
			"outbox: nil publisher for DirectEmitter")
	}
	if mp == nil {
		return nil, errcode.New(errcode.KindInternal, errcode.ErrCellMissingOutbox,
			"outbox: nil metrics provider for DirectEmitter")
	}
	clock.MustHaveClock(clk, "outbox.NewDirectEmitter")
	if cellID == "" {
		return nil, errcode.New(errcode.KindInvalid, errcode.ErrValidationFailed,
			"outbox: cellID must not be empty for DirectEmitter")
	}
	cv, err := mp.CounterVec(metrics.CounterOpts{
		Name:       "outbox_emit_failopen_dropped_total",
		Help:       "Total outbox entries dropped in fail-open mode. cell=Cell ID; topic=routing topic.",
		LabelNames: []string{"cell", "topic"},
	})
	if err != nil {
		return nil, fmt.Errorf("outbox: register failopen_dropped counter: %w", err)
	}
	cfg := &directEmitterOptions{
		logger:             slog.Default(),
		failOpenRateThresh: defaultFailOpenRateThreshold,
	}
	for _, opt := range opts {
		opt(cfg)
	}
	if cfg.logger == nil {
		cfg.logger = slog.Default() // 防御 WithLogger(nil)
	}
	return &DirectEmitter{
		publisher:         p,
		mode:              mode,
		clock:             clk,
		cellID:            cellID,
		logger:            cfg.logger,
		failOpenDroppedCv: cv,
		failOpenTracker:   newFailOpenTracker(cfg.failOpenRateThresh),
	}, nil
}

// Emit validates the entry, injects observability metadata from ctx, marshals
// the v1 wire envelope, and publishes synchronously. When publish fails, the
// per-entry FailurePolicy (or the construction-time default) decides between
// fail-closed (return the wrapped error) and fail-open (log + increment the
// gocell_outbox_emit_failopen_dropped_total counter and return nil so the
// caller's request path is not blocked on broker availability).
func (e *DirectEmitter) Emit(ctx context.Context, entry Entry) error {
	if e == nil || isNilEmitterDependency(e.publisher) {
		return errcode.New(errcode.KindInternal, errcode.ErrCellMissingOutbox,
			"outbox: nil publisher for DirectEmitter")
	}
	if err := entry.Validate(); err != nil {
		return err
	}
	if entry.CreatedAt.IsZero() {
		entry.CreatedAt = e.clock.Now().UTC()
	}
	// Inject observability from context right before publishing so the entry
	// carries the originating request's trace/request/correlation identity
	// across the async boundary. Mirrors adapters/postgres/outbox_writer.go:61.
	entry.InjectObservabilityFromContext(ctx)
	envelope, err := MarshalEnvelope(entry)
	if err != nil {
		return err
	}
	topic := entry.RoutingTopic()
	if err := e.publisher.Publish(ctx, topic, envelope); err != nil {
		// Per-entry FailurePolicy wins over construction-time default.
		// Security / audit-chain events set FailurePolicyFailClosed at
		// entry-construction time; observability events may opt into
		// FailurePolicyFailOpen. Zero value falls through to e.mode.
		// ref: k8s apiserver/pkg/audit Backend.FailurePolicy model.
		mode := entry.FailurePolicy.Resolve(e.mode)
		if mode == DirectPublishFailOpen {
			e.logger.Warn(WarnDirectPublishFailOpen,
				slog.String("topic", topic),
				slog.String("entry_id", entry.ID),
				slog.String("event_type", entry.EventType),
				slog.Any("error", err))
			e.failOpenDroppedCv.With(metrics.Labels{
				"cell":  e.cellID,
				"topic": topic,
			}).Inc()
			e.failOpenTracker.RecordDrop()
			return nil
		}
		return fmt.Errorf("outbox: direct publish failed for topic %s: %w", topic, err)
	}
	e.failOpenTracker.RecordSuccess()
	return nil
}

var _ Emitter = (*DirectEmitter)(nil)

// ErrDegraded is the canonical sentinel returned by DirectEmitter.Probes
// to signal "operational but degraded" — the emitter is still serving requests
// but the fail-open drop ratio has exceeded the configured threshold, meaning
// events are silently lost at an elevated rate.
//
// The /readyz aggregator detects ErrDegraded via errors.Is and maps it to
// HTTP 200 + body status="degraded" rather than 503, so K8s readinessProbe
// does not evict the pod for soft-failure signals.
//
// kernel/cell.ErrDegraded is an alias to this sentinel so that callers
// referencing either symbol satisfy the same errors.Is chain.
//
// ref: envoyproxy/envoy admin /ready — DEGRADED returns 200, distinguishing
// "soft failure, do not evict" from "hard failure, drain traffic".
// ref: kernel/cell/health.go — cell-layer alias to this sentinel.
var ErrDegraded = errcode.New(errcode.KindUnavailable, errcode.ErrOutboxDegraded, "degraded")

// Probes returns a probe map for cells to register via reg.Health(...). The
// probe name is scoped by cellID to avoid collisions when multiple cells own a
// DirectEmitter (each /readyz checker name MUST be globally unique).
//
// The probe returns ErrDegraded when the fail-open drop ratio exceeds the
// threshold configured via WithFailOpenRateThreshold (default 5%). The /readyz
// aggregator detects this via errors.Is and maps to HTTP 200 + status="degraded"
// rather than 503.
//
// Implementation note: the underlying tracker uses delta semantics — two
// consecutive /readyz probes with no new emit between them yield ratio
// 0/0 → not tripped (preserve healthy state). Operators monitoring
// degraded transitions via /readyz should not interpret a return-to-healthy
// as "drops have stopped"; pair with the gocell_outbox_emit_failopen_dropped_total
// counter for the actionable signal.
//
// ref: kernel/outbox/emitter.go ErrDegraded
// ref: cells/accesscore/cell_providers.go:22-38 — kebab-case checker name convention
func (e *DirectEmitter) Probes() map[string]func(context.Context) error {
	return map[string]func(context.Context) error{
		"outbox-failopen-rate." + e.cellID: e.checkFailOpenRate,
	}
}

func (e *DirectEmitter) checkFailOpenRate(_ context.Context) error {
	if e.failOpenTracker.Tripped() {
		return fmt.Errorf("outbox: fail-open drop ratio exceeded threshold for cell %q: %w", e.cellID, ErrDegraded)
	}
	return nil
}

// DurabilityReporter is an optional interface Emitter implementations may
// expose so callers (typically Cell boundaries) can query whether this
// emitter is backed by durable (transactional outbox) sinks. Emitters that
// do not implement DurabilityReporter are treated as non-durable by callers
// — the safe default for direct-publish and noop paths.
//
// ref: kernel/cell.EmitterOutcome.Durable — the primary consumer; Cells
// use the reported value to decide whether optional slices (e.g. rbacassign)
// upgrade from L0 to L2.
// ref: github.com/ThreeDotsLabs/watermill message/router.go —
// `disabledPublisher` pattern; an explicit typed indicator lets callers
// branch on capability without runtime type switches.
type DurabilityReporter interface {
	Durable() bool
}

// Durable reports whether this WriterEmitter is backed by a real (non-noop)
// outbox.Writer. NoopWriter and any writer that advertises Noop()==true are
// considered non-durable; anything else is durable.
func (e *WriterEmitter) Durable() bool {
	if e == nil || e.writer == nil {
		return false
	}
	n, ok := e.writer.(interface{ Noop() bool })
	if !ok {
		return true
	}
	return !n.Noop()
}

// Durable always returns false for DirectEmitter: direct publishing bypasses
// the transactional outbox and therefore carries no durability guarantee by
// design.
func (*DirectEmitter) Durable() bool { return false }

// ReportDurable returns the durability status of an Emitter, defaulting to
// false when the implementation does not expose DurabilityReporter. Intended
// for Cell boundaries that accept a directly-injected Emitter via WithEmitter
// but still need to decide L2/L0 slice upgrades based on durability.
func ReportDurable(e Emitter) bool {
	if e == nil {
		return false
	}
	r, ok := e.(DurabilityReporter)
	return ok && r.Durable()
}

var (
	_ DurabilityReporter = (*WriterEmitter)(nil)
	_ DurabilityReporter = (*DirectEmitter)(nil)
)

func isNilEmitterDependency(v any) bool {
	if v == nil {
		return true
	}
	rv := reflect.ValueOf(v)
	switch rv.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return rv.IsNil()
	default:
		return false
	}
}
