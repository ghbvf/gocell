package transport

import (
	"reflect"
	"testing"
)

// INVARIANT: INPROCESS-TRANSPORT-SEALED-01
//
// TestInProcessTransportZeroExportedFields enforces the sealed-construction
// invariant (ADR D2 upstream Hard): an InProcessTransport must have zero exported
// fields so the only way to populate one is via NewInProcess (the sole
// constructor) + Bind. If any field were exported, package-external code could
// forge a transport via a struct literal — setting the handler directly and
// bypassing the WriteOnce bind, or fabricating a transport to escape the
// downstream funnel (CELL-SYNC-TRANSPORT-FUNNEL-01). Adding an exported field
// trips this reflect freeze.
func TestInProcessTransportZeroExportedFields(t *testing.T) {
	t.Parallel()

	rt := reflect.TypeOf(InProcessTransport{})
	for i := 0; i < rt.NumField(); i++ {
		if f := rt.Field(i); f.IsExported() {
			t.Errorf("INPROCESS-TRANSPORT-SEALED-01: InProcessTransport has exported field %q (%s) — "+
				"all fields must be unexported so NewInProcess+Bind is the only construction path; "+
				"an exported field lets package-external code forge a transport via struct literal",
				f.Name, f.Type)
		}
	}

	// Anti-vacuity: the type must actually have fields (a future refactor to a
	// fieldless struct would make the loop vacuously pass while silently dropping
	// the seal's subject).
	if rt.NumField() == 0 {
		t.Error("INPROCESS-TRANSPORT-SEALED-01: InProcessTransport has no fields — the seal check is vacuous")
	}
}
