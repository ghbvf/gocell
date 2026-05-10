package outbox

import "github.com/ghbvf/gocell/pkg/validation"

// CellPublisher is the only Publisher-shaped type that cells/<x>/cell.go
// public With* Options may accept. The unexported sealedCellPublisher()
// method makes CellPublisher unimplementable outside this package —
// kernel/outbox is the sole entry point via WrapPublisherForCell, which
// composition roots must call.
//
// AI-HARD per ai-collab.md §"违反不可表达": writing
// `WithFoo(pub outbox.Publisher) Option` in any cell.go and trying to
// wire it from a composition root will fail at compile time — the type
// system rejects assignment of a raw Publisher to CellPublisher, and
// external packages cannot implement CellPublisher.
//
// CellPublisher embeds Publisher so cells can pass the wrapped value
// directly to outbox.NewDirectEmitter etc. — those constructors accept
// Publisher and the embedded interface satisfies them transparently, so
// internal kernel/outbox API does not change.
//
// ref: docs/architecture/<adr-cell-raw-infra-sealed-marker>.md §D1
type CellPublisher interface {
	Publisher
	sealedCellPublisher()
}

// CellWriter mirrors CellPublisher for the outbox.Writer side: cells/<x>
// public With* Options accept CellWriter; raw Writer is sealed off behind
// WrapWriterForCell, callable only from composition roots.
type CellWriter interface {
	Writer
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
//   - examples/<demo>/main.go and examples/<demo>/app.go composition roots
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
