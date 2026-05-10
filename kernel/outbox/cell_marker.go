package outbox

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

type internalCellWriter struct {
	Writer
}

func (internalCellWriter) sealedCellWriter() {}

// WrapPublisherForCell is the sole authorized path for handing a Publisher
// to a cell's With* Option. Returns nil when p is nil so caller-side
// typed-nil detection keeps working in accumulative builder options
// (e.g. WithOutboxDeps(WrapPublisherForCell(nil), ...) is a no-op for the
// publisher slot, leaving any previously-set value in place).
//
// Allowed callers (enforced by archtest CELL-RAW-INFRA-WRAPPER-LOCATION-01):
//   - cmd/* composition roots
//   - examples/<demo>/main.go and examples/<demo>/app.go composition roots
//   - *_test.go in any layer
//   - kernel/outbox/cell_marker.go (this file)
func WrapPublisherForCell(p Publisher) CellPublisher {
	if p == nil {
		return nil
	}
	return internalCellPublisher{Publisher: p}
}

// WrapWriterForCell mirrors WrapPublisherForCell for the Writer side.
func WrapWriterForCell(w Writer) CellWriter {
	if w == nil {
		return nil
	}
	return internalCellWriter{Writer: w}
}
