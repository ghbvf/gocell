package outbox

import (
	"github.com/ghbvf/gocell/kernel/healthz"
	"github.com/ghbvf/gocell/pkg/validation"
)

// CellPublisher is the only Publisher-shaped type that cells/<x>/cell.go
// public With* Options may accept. The unexported sealedCellPublisher()
// method makes CellPublisher unimplementable outside this package —
// kernel/outbox is the sole entry point via WrapPublisherForCell, which
// composition roots must call.
//
// AI-robust 评级：
//   - 字段/赋值层：Hard（sealed marker，外部不可表达 internalCellPublisher 字面量）
//   - 公开 With* Option 签名层：Medium（CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01 archtest type-aware 守）
//
// 双重防线，参见 ADR 202605101900-adr-cell-raw-infra-sealed-marker §D2。
//
// CellPublisher embeds Publisher so cells can pass the wrapped value
// directly to outbox.NewDirectEmitter etc. — those constructors accept
// Publisher and the embedded interface satisfies them transparently, so
// internal kernel/outbox API does not change.
//
// ref: docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md §D1
type CellPublisher interface {
	Publisher
	// MARKER: do not implement; this is the sealing marker — call outbox.WrapPublisherForCell(...) from your composition root instead.
	sealedCellPublisher()
}

// CellWriter mirrors CellPublisher for the outbox.Writer side: cells/<x>
// public With* Options accept CellWriter; raw Writer is sealed off behind
// WrapWriterForCell, callable only from composition roots.
//
// ref: docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md §D1
type CellWriter interface {
	Writer
	// MARKER: do not implement; this is the sealing marker — call outbox.WrapWriterForCell(...) from your composition root instead.
	sealedCellWriter()
}

type internalCellPublisher struct {
	Publisher
}

func (internalCellPublisher) sealedCellPublisher() {}

// Noop transparently delegates to the wrapped Publisher's Nooper interface
// when present (kernel/cell.Nooper). Without this, the sealed wrapper would
// hide outbox.DiscardPublisher's noop signal from cell.CheckNotNoop and
// kernel/cell/mode_resolver.go's isNooperDep, letting durable assemblies
// silently accept demo publishers.
func (i internalCellPublisher) Noop() bool {
	type nooper interface{ Noop() bool }
	if n, ok := i.Publisher.(nooper); ok {
		return n.Noop()
	}
	return false
}

type internalCellWriter struct {
	Writer
}

func (internalCellWriter) sealedCellWriter() {}

// Noop transparently delegates to the wrapped Writer's Nooper interface
// when present. Mirror of internalCellPublisher.Noop — see that godoc for
// the rationale (preserve cell.CheckNotNoop and isNooperDep detection).
func (i internalCellWriter) Noop() bool {
	type nooper interface{ Noop() bool }
	if n, ok := i.Writer.(nooper); ok {
		return n.Noop()
	}
	return false
}

// WrapPublisherForCell is the sole authorized path for handing a Publisher
// to a cell's With* Option. Returns nil when p is bare-nil OR a typed-nil
// interface (e.g. `var p *amqpPublisher`) so caller-side typed-nil
// detection keeps working in accumulative builder options. Without
// IsNilInterface the wrapper would emit a non-nil sealed value hiding the
// inner nil, silently bypassing Init() fail-fast guards and panicking on
// the first Publish call.
//
// Allowed callers (enforced by archtest CELL-RAW-INFRA-WRAPPER-LOCATION-01):
//   - cmd/* composition roots
//   - examples/<demo>/main.go, examples/<demo>/app.go, and examples/<demo>/run.go composition roots
//     (run.go is the hand-written half of the K#10 main+run split)
//   - *_test.go in any layer
//   - kernel/outbox/cell_marker.go (this file)
func WrapPublisherForCell(p Publisher) CellPublisher {
	if validation.IsNilInterface(p) {
		return nil
	}
	return internalCellPublisher{Publisher: p}
}

// WrapWriterForCell mirrors WrapPublisherForCell for the Writer side.
// Bare-nil and typed-nil are both rejected via validation.IsNilInterface.
func WrapWriterForCell(w Writer) CellWriter {
	if validation.IsNilInterface(w) {
		return nil
	}
	return internalCellWriter{Writer: w}
}

// CellEmitter is the only Emitter-shaped type that cells/<x> public With*
// Options (Cell.WithEmitter + every slice WithEmitter) may accept. The
// unexported sealedCellEmitter() method makes it unimplementable outside this
// package — kernel/outbox is the sole entry point via WrapEmitterForCell,
// which composition roots and the kernel-internal ResolveCellEmitter funnel
// call.
//
// Unlike CellPublisher / CellWriter, CellEmitter additionally embeds
// DurabilityReporter and healthz.ProbeSet. Embedding the bare Emitter
// interface in internalCellEmitter promotes only Emit, so the inner
// *WriterEmitter.Durable() and *DirectEmitter.Probes() would be hidden behind
// the wrapper. Listing both in the CellEmitter contract makes the forwarding
// methods on internalCellEmitter COMPILE-MANDATORY (AI-HARD): omit Durable()
// or Probes() and internalCellEmitter no longer satisfies CellEmitter, so
// WrapEmitterForCell fails to compile. Without this a missing forward would
// silently break L2 durability detection (ResolveCellEmitter) and emitter
// health-probe registration (cell.RegisterEmitterHealthProbes).
//
// AI-robust 评级：
//   - 字段/赋值层 + Durable()/Probes() forwarding：Hard（sealed marker 使外部
//     不可表达 internalCellEmitter 字面量；interface 嵌入使内部不可省略 forwarding）
//   - 公开 With* Option 签名层：Medium（CELL-RAW-INFRA-PUBLIC-OPTION-PARAM-01）
//   - Noop() 透传：Hard（SEALED-MARKER-NOOP-TRANSPARENCY-01 typed auto-discovery）
//
// CellEmitter embeds Emitter so cells pass the wrapped value directly to slice
// service constructors / Emit call sites — those accept Emitter and the
// embedded interface satisfies them transparently, so internal API does not
// change.
//
// ref: docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md §D1
type CellEmitter interface {
	Emitter
	DurabilityReporter
	healthz.ProbeSet
	// MARKER: do not implement; this is the sealing marker — call outbox.WrapEmitterForCell(...) from your composition root instead.
	sealedCellEmitter()
}

type internalCellEmitter struct {
	Emitter
}

func (internalCellEmitter) sealedCellEmitter() {}

// Noop transparently delegates to the wrapped Emitter's Nooper interface when
// present. No production Emitter implements Nooper today (emitter durability is
// reported via DurabilityReporter, not Nooper), but the uniform internalCell*
// transparency contract (SEALED-MARKER-NOOP-TRANSPARENCY-01) requires every
// sealed marker to forward Nooper so a future Nooper-capable Emitter is not
// silently hidden. Mirror of internalCellPublisher.Noop.
func (i internalCellEmitter) Noop() bool {
	type nooper interface{ Noop() bool }
	if n, ok := i.Emitter.(nooper); ok {
		return n.Noop()
	}
	return false
}

// Durable forwards the wrapped Emitter's DurabilityReporter signal via
// ReportDurable (false when the inner Emitter does not implement it).
// Required because embedding the Emitter interface drops the inner
// *WriterEmitter / *DirectEmitter Durable() method; ResolveCellEmitter and L2
// atomicity decisions read durability through the wrap. Delegating to
// ReportDurable keeps the optional-interface query single-sourced.
func (i internalCellEmitter) Durable() bool {
	return ReportDurable(i.Emitter)
}

// Probes forwards the wrapped Emitter's healthz.ProbeSet (nil when the inner
// Emitter exposes no probes, e.g. WriterEmitter). Required because embedding
// the Emitter interface drops the inner *DirectEmitter.Probes() method;
// cell.RegisterEmitterHealthProbes registers these through the wrap.
func (i internalCellEmitter) Probes() []healthz.Probe {
	if ps, ok := i.Emitter.(healthz.ProbeSet); ok {
		return ps.Probes()
	}
	return nil
}

// WrapEmitterForCell is the sole authorized path for handing an Emitter to a
// cell's With* Option. Returns nil when e is bare-nil OR a typed-nil interface
// (e.g. `var e *DirectEmitter`) so caller-side typed-nil detection keeps
// working in accumulative builder options — mirror of WrapPublisherForCell.
//
// Allowed callers (enforced by archtest CELL-RAW-INFRA-WRAPPER-LOCATION-01):
//   - cmd/* composition roots
//   - examples/<demo>/main.go, examples/<demo>/app.go, examples/<demo>/run.go
//   - *_test.go in any layer
//   - kernel/outbox/cell_marker.go (this file)
//   - kernel/outbox/mode_resolver.go (ResolveCellEmitter resolution funnel,
//     which wraps the kernel-built DirectEmitter/WriterEmitter so cells hold a
//     sealed CellEmitter uniformly)
func WrapEmitterForCell(e Emitter) CellEmitter {
	if validation.IsNilInterface(e) {
		return nil
	}
	return internalCellEmitter{Emitter: e}
}
