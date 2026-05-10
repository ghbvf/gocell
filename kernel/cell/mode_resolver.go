package cell

import (
	"fmt"
	"log/slog"

	"github.com/ghbvf/gocell/kernel/cellvocab"
	"github.com/ghbvf/gocell/kernel/clock"
	"github.com/ghbvf/gocell/kernel/observability/metrics"
	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
)

const internalCellPlainFmt = "cell=%s"

// EmitterConfig bundles the inputs needed to resolve an outbox.Emitter
// for a Cell according to DurabilityMode rules.
//
// ResolveEmitter accepts nil TxRunner and treats it as absent (no pairing with
// a real writer is possible). Callers in Durable mode must inject a real
// TxRunner explicitly; Demo mode tolerates nil and any Nooper implementation.
//
// MetricsProvider is required for DirectEmitter resolution paths. Pass
// metrics.NopProvider{} explicitly in tests.
//
// ref: kernel/cell.CheckNotNoop — sibling durability guard.
type EmitterConfig struct {
	CellID            string
	Mode              DurabilityMode
	Publisher         outbox.Publisher
	OutboxWriter      outbox.Writer
	TxRunner          persistence.TxRunner
	Logger            *slog.Logger
	DirectPublishMode outbox.DirectPublishFailureMode
	// MetricsProvider is REQUIRED when DurabilityDemo mode resolves to a DirectEmitter.
	MetricsProvider metrics.Provider
	// Clock is the time source injected into DirectEmitter for CreatedAt stamping.
	// Required when DurabilityDemo mode resolves to a DirectEmitter; pass
	// clock.Real() in production and clockmock.New(...) in tests.
	Clock clock.Clock
}

// EmitterOutcome reports the resolved emitter and whether it is durable
// (backed by a real writer+txRunner). Cells use Durable to upgrade optional
// slices from cellvocab.L0 to cellvocab.L2 (e.g., rbacassign).
type EmitterOutcome struct {
	Emitter outbox.Emitter
	Durable bool
}

// ResolveEmitter picks the right outbox.Emitter based on durability mode,
// publisher, writer, and txRunner. Semantics mirror the pre-existing
// per-cell resolveOutboxDeps+resolveDemoEmitter pair that accesscore/
// configcore/auditcore each carried.
//
// Durable mode: requires real writer+txRunner (non-noop); nil/noop → error.
//
// Demo mode: prefers DirectEmitter when publisher is present and writer is
// absent-or-noop. Falls back to WriterEmitter when writer is present (paired
// with txRunner — both together form a valid demo sink). Both absent → error.
//
// ref: kernel/cell.CheckNotNoop — sibling durability guard.
// ref: github.com/ThreeDotsLabs/watermill message/router.go — disabledPublisher pattern.
func ResolveEmitter(cfg EmitterConfig) (EmitterOutcome, error) {
	// Durability gate: rejects noop deps in durable mode, validates mode value.
	if err := CheckNotNoop(cfg.Mode, cfg.CellID, cfg.OutboxWriter, cfg.TxRunner, cfg.Publisher); err != nil {
		return EmitterOutcome{}, err
	}

	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}

	if cfg.Mode == DurabilityDurable {
		return resolveDurableEmitter(cfg)
	}
	return resolveDemoEmitter(cfg, logger)
}

// resolveDurableEmitter handles DurabilityDurable mode.
// CheckNotNoop has already rejected nooper deps; we only need nil checks here.
func resolveDurableEmitter(cfg EmitterConfig) (EmitterOutcome, error) {
	if cfg.OutboxWriter == nil || cfg.TxRunner == nil {
		return EmitterOutcome{}, errcode.New(errcode.KindInternal, errcode.ErrCellMissingOutbox,
			"durable mode requires real outboxWriter and txRunner",
			errcode.WithInternal(fmt.Sprintf(internalCellPlainFmt, cfg.CellID)))
	}
	emitter, err := outbox.NewWriterEmitter(cfg.OutboxWriter)
	if err != nil {
		return EmitterOutcome{}, err
	}
	return EmitterOutcome{Emitter: emitter, Durable: true}, nil
}

// resolveDemoEmitter handles DurabilityDemo mode.
// Applies pairing invariant: OutboxWriter and TxRunner must be provided together.
func resolveDemoEmitter(cfg EmitterConfig, logger *slog.Logger) (EmitterOutcome, error) {
	// Pairing invariant: writer and txRunner must be together.
	writerAbsent := cfg.OutboxWriter == nil
	txAbsent := cfg.TxRunner == nil
	if writerAbsent != txAbsent {
		return EmitterOutcome{}, errcode.New(errcode.KindInternal, errcode.ErrCellMissingOutbox,
			"demo mode requires outboxWriter and txRunner together; inject both explicitly",
			errcode.WithInternal(fmt.Sprintf(internalCellPlainFmt, cfg.CellID)))
	}

	// No sink at all.
	if cfg.Publisher == nil && cfg.OutboxWriter == nil {
		return EmitterOutcome{}, errcode.New(errcode.KindInternal, errcode.ErrCellMissingOutbox,
			"demo mode requires an explicit event sink; provide publisher or outboxWriter+txRunner",
			errcode.WithInternal(fmt.Sprintf(internalCellPlainFmt, cfg.CellID)))
	}

	// Publisher-preferred path: publisher present and writer absent or noop.
	if cfg.Publisher != nil && (cfg.OutboxWriter == nil || isNooperDep(cfg.OutboxWriter)) {
		if cfg.MetricsProvider == nil {
			return EmitterOutcome{}, errcode.New(errcode.KindInternal, errcode.ErrCellMissingOutbox,
				"demo mode with direct publisher requires MetricsProvider; "+
					"pass kernel/observability/metrics.NopProvider{} explicitly "+
					"in tests (e.g. via cells/{accesscore,auditcore,configcore}."+
					"WithMetricsProvider(...))",
				errcode.WithInternal(fmt.Sprintf(internalCellPlainFmt, cfg.CellID)))
		}
		emitter, err := outbox.NewDirectEmitter(
			cfg.Publisher, cfg.DirectPublishMode, cfg.MetricsProvider,
			cfg.Clock, cfg.CellID, outbox.WithLogger(logger),
		)
		if err != nil {
			return EmitterOutcome{}, err
		}
		return EmitterOutcome{Emitter: emitter, Durable: false}, nil
	}

	// Writer path: use WriterEmitter; durable only when writer is real (non-noop).
	if cfg.OutboxWriter != nil {
		emitter, err := outbox.NewWriterEmitter(cfg.OutboxWriter)
		if err != nil {
			return EmitterOutcome{}, err
		}
		return EmitterOutcome{Emitter: emitter, Durable: !isNooperDep(cfg.OutboxWriter)}, nil
	}

	return EmitterOutcome{}, errcode.New(errcode.KindInternal, errcode.ErrCellMissingOutbox,
		"demo mode requires an explicit event sink",
		errcode.WithInternal(fmt.Sprintf(internalCellPlainFmt, cfg.CellID)))
}

// isNooperDep returns true when dep implements Nooper and reports Noop()==true.
// Uses the exported Nooper interface defined in durability.go.
func isNooperDep(dep any) bool {
	n, ok := dep.(Nooper)
	return ok && n.Noop()
}

// CellEmitterInputs bundles the Cell-side inputs for ResolveCellEmitter.
// Embeds EmitterConfig and adds the two knobs shared by every Cell's
// Init-time emitter resolution: the pre-resolved emitter (WithEmitter) and
// the Cell's consistency level (for the cellvocab.L2 non-durable Warn).
type CellEmitterInputs struct {
	EmitterConfig
	// PreResolved is the emitter set directly via Cell.WithEmitter(e).
	// When non-nil, ResolveCellEmitter skips ResolveEmitter and validates that
	// durable mode requires a durable PreResolved (ReportDurable==true).
	PreResolved outbox.Emitter
	// ConsistencyLevel is the owning Cell's consistency level; used to decide
	// whether the cellvocab.L2 non-durable Warn log fires.
	ConsistencyLevel cellvocab.Level
}

// ResolveCellEmitter is the Cell-side wrapper around ResolveEmitter that
// enforces the contract shared by accesscore/auditcore/configcore:
//
//  1. PreResolved (WithEmitter) and raw deps (WithOutboxDeps) are mutually
//     exclusive — both set returns ErrCellInvalidConfig.
//  2. PreResolved + durable mode requires a durable emitter; otherwise returns
//     ErrCellMissingOutbox.
//  3. Otherwise delegate to ResolveEmitter.
//  4. When the resolved emitter is non-durable and the Cell's consistency
//     level is cellvocab.L2 or higher, emit a Warn explaining the degraded atomicity
//     guarantee. The log carries cell, consistency_level, durability_mode.
//
// Per-cell side-effects (e.g. AccessCore.rbacEmitterMode) remain at the call
// site and read outcome.Durable from the return value.
//
// ref: kernel/cell.ResolveEmitter — the primitive this wraps.
func ResolveCellEmitter(in CellEmitterInputs) (EmitterOutcome, error) {
	hasEmitter := in.PreResolved != nil
	hasPending := in.Publisher != nil || in.OutboxWriter != nil
	if hasEmitter && hasPending {
		return EmitterOutcome{}, errcode.New(errcode.KindInternal, errcode.ErrCellInvalidConfig,
			"WithEmitter and WithOutboxDeps are mutually exclusive; pick exactly one",
			errcode.WithInternal(fmt.Sprintf(internalCellPlainFmt, in.CellID)))
	}

	var outcome EmitterOutcome
	if hasEmitter {
		durable := outbox.ReportDurable(in.PreResolved)
		if in.Mode == DurabilityDurable && !durable {
			return EmitterOutcome{}, errcode.New(errcode.KindInternal, errcode.ErrCellMissingOutbox,
				"WithEmitter in durable mode requires a durable outbox.Emitter (WriterEmitter over real writer); got non-durable emitter",
				errcode.WithInternal(fmt.Sprintf(internalCellPlainFmt, in.CellID)))
		}
		outcome = EmitterOutcome{Emitter: in.PreResolved, Durable: durable}
	} else {
		resolved, err := ResolveEmitter(in.EmitterConfig)
		if err != nil {
			return EmitterOutcome{}, err
		}
		outcome = resolved
	}

	if !outcome.Durable && in.ConsistencyLevel >= cellvocab.L2 {
		logger := in.Logger
		if logger == nil {
			logger = slog.Default()
		}
		logger.Warn(in.CellID+": running without outboxWriter+txRunner, cellvocab.L2 transactional atomicity not guaranteed (demo mode)",
			slog.String("cell", in.CellID),
			slog.Int("consistency_level", int(in.ConsistencyLevel)),
			slog.String("durability_mode", in.Mode.String()))
	}
	return outcome, nil
}
