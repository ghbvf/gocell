package cell

import (
	"log/slog"

	"github.com/ghbvf/gocell/kernel/outbox"
	"github.com/ghbvf/gocell/kernel/persistence"
	"github.com/ghbvf/gocell/pkg/errcode"
)

// EmitterConfig bundles the inputs needed to resolve an outbox.Emitter
// for a Cell according to DurabilityMode rules.
//
// Callers should pre-apply persistence.RunnerOrNoop(txRunner) before passing
// when they want a unified code path; ResolveEmitter itself accepts nil TxRunner
// and treats it as absent (no pairing with a real writer is possible).
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
}

// EmitterOutcome reports the resolved emitter and whether it is durable
// (backed by a real writer+txRunner). Cells use Durable to upgrade optional
// slices from L0 to L2 (e.g., rbacassign).
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
		return EmitterOutcome{}, errcode.New(errcode.ErrCellMissingOutbox,
			cfg.CellID+" durable mode requires real outboxWriter and txRunner")
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
		return EmitterOutcome{}, errcode.New(errcode.ErrCellMissingOutbox,
			cfg.CellID+" demo mode requires outboxWriter and txRunner together; inject both explicitly")
	}

	// No sink at all.
	if cfg.Publisher == nil && cfg.OutboxWriter == nil {
		return EmitterOutcome{}, errcode.New(errcode.ErrCellMissingOutbox,
			cfg.CellID+" demo mode requires an explicit event sink; provide publisher or outboxWriter+txRunner")
	}

	// Publisher-preferred path: publisher present and writer absent or noop.
	if cfg.Publisher != nil && (cfg.OutboxWriter == nil || isNooperDep(cfg.OutboxWriter)) {
		emitter, err := outbox.NewDirectEmitter(cfg.Publisher, cfg.DirectPublishMode, logger)
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

	return EmitterOutcome{}, errcode.New(errcode.ErrCellMissingOutbox,
		cfg.CellID+" demo mode requires an explicit event sink")
}

// isNooperDep returns true when dep implements Nooper and reports Noop()==true.
// Uses the exported Nooper interface defined in durability.go.
func isNooperDep(dep any) bool {
	n, ok := dep.(Nooper)
	return ok && n.Noop()
}

// DirectPublishModeForDurability picks the DirectPublishFailureMode a Cell
// should request from its DirectEmitter based on durability intent. Durable
// mode returns durablePolicy; DurabilityDemo and any other (unknown) mode
// value returns demoPolicy. The default-to-demo fallback is deliberate — any
// future DurabilityMode value gets the safer non-failing policy by default,
// and callers adding new modes must extend this function explicitly rather
// than rely on silent fallthrough.
//
// Centralizes the translation that accesscore/auditcore previously hard-coded
// inline and configcore expressed via a per-cell helper, so all three cells
// share one semantic: "demo uses demoPolicy; durable uses durablePolicy"
// comes from a single implementation.
//
// ref: kernel/cell.ResolveEmitter — consumes the resulting fail mode.
func DirectPublishModeForDurability(
	mode DurabilityMode,
	demoPolicy outbox.DirectPublishFailureMode,
	durablePolicy outbox.DirectPublishFailureMode,
) outbox.DirectPublishFailureMode {
	if mode == DurabilityDurable {
		return durablePolicy
	}
	return demoPolicy
}
