package transport

import (
	"reflect"
	"testing"
)

// INVARIANT: INPROCESS-TRANSPORT-SEALED-01
// INVARIANT: REMOTE-TRANSPORT-SEALED-01
//
// # INPROCESS-TRANSPORT-SEALED-01 — InProcessTransport is a sealed type (Hard)
//
// ## Rule
//
// An InProcessTransport must have ZERO exported fields, so the only way to
// populate one is via NewInProcess (the sole constructor) + Bind (the sole
// atomic publish). If any field were exported, package-external code could forge
// a transport via a struct literal — setting the handler directly and bypassing
// the WriteOnce bind, or fabricating a transport to escape the downstream funnel.
//
// ## Why (the UPSTREAM Hard half of the sync-transport funnel)
//
// This is the upstream Hard counterpart to the downstream Medium
// CELL-SYNC-TRANSPORT-FUNNEL-01: a transport cannot be FORGED (this rule), and a
// cell cannot BYPASS it with a raw http client (the funnel) — together a closed
// funnel. The seal is enforced by the type system (unexported fields are a
// compile-time bar to external struct-literal construction); this reflect freeze
// pins the field set so a future edit that exports a field trips the test.
//
// ## AI-robust rating: Hard (sealed construction + reflect field freeze)
//
// Unexported-only fields make external construction a compile error (Hard); the
// reflect freeze backstops a regression that adds an exported field.
//
// ## Blind spots
//
//   - Package-internal struct-literal construction (an in-package bug that builds
//     an InProcessTransport{} bypassing NewInProcess) is the Go visibility
//     ceiling — not reachable by external code; this rule is scoped to the
//     external-forgery threat the funnel cares about.
//
// ## Anti-vacuity
//
//   - The NumField()==0 guard fails the test if a future refactor makes the
//     struct fieldless (which would make the exported-field loop vacuously pass
//     while dropping the seal's subject).
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

// REMOTE-TRANSPORT-SEALED-01 — RemoteHTTPTransport is a sealed type (Hard)
//
// # Rule
//
// A RemoteHTTPTransport must have ZERO exported fields, so the only way to
// obtain a usable value is via NewRemoteHTTP (the sole constructor). If any
// field were exported, package-external code could forge a transport via a
// struct literal — bypassing the nil-resolver / nil-client / empty-targetCellID
// guards that enforce the construction invariants.
//
// # Why (upstream Hard half of the remote-transport funnel)
//
// This is the upstream Hard counterpart to CELLTRANSPORT-SELECT-FUNNEL-01
// (Medium, archtest): a transport cannot be FORGED (this rule), and wiring
// code cannot bypass celltransport.Resolve to construct a NewRemoteHTTP
// directly (the funnel). Together they form a closed funnel. The seal is
// enforced by the type system; this reflect freeze pins the field set so a
// future edit that exports a field trips the test.
//
// # AI-robust rating: Hard (sealed construction + reflect field freeze)
//
// # Anti-vacuity
//
// The NumField()==0 guard fails the test if a future refactor makes the
// struct fieldless (which would make the exported-field loop vacuously pass).
func TestRemoteHTTPTransportZeroExportedFields(t *testing.T) {
	t.Parallel()

	rt := reflect.TypeOf(RemoteHTTPTransport{})
	for i := 0; i < rt.NumField(); i++ {
		if f := rt.Field(i); f.IsExported() {
			t.Errorf("REMOTE-TRANSPORT-SEALED-01: RemoteHTTPTransport has exported field %q (%s) — "+
				"all fields must be unexported so NewRemoteHTTP is the only construction path; "+
				"an exported field lets package-external code forge a transport via struct literal",
				f.Name, f.Type)
		}
	}

	if rt.NumField() == 0 {
		t.Error("REMOTE-TRANSPORT-SEALED-01: RemoteHTTPTransport has no fields — the seal check is vacuous")
	}
}
