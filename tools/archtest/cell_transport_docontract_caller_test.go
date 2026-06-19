//go:build archtest

// INVARIANT: CELL-TRANSPORT-DOCONTRACT-CALLER-01
//
// # CELL-TRANSPORT-DOCONTRACT-CALLER-01 — cell production code must not call transport.CellTransport.DoContract directly (Medium)
//
// ## Rule
//
// A cell's production code MUST NOT call transport.CellTransport.DoContract
// directly. No package owned by a cell (Classifier.Cell(pkg) != "") may, in a
// non-test production file, invoke the DoContract method declared by the
// runtime/transport package. The sanctioned callers are the codegen-generated
// contract clients under generated/contracts/** (not cell-owned packages), so the
// cell-scoped scan excludes them by construction.
//
// Rationale (epic #1423 US4 follow-up #2093 — generated-client downstream Hard):
// the codegen-generated contract client is the SOLE generated sibling-cell call
// type (ADR D2 downstream — Hard via codegen + byte golden, NOT interface sealing;
// transport.CellTransport is a plain exported interface). It holds an injected
// transport.CellTransport and calls DoContract internally; a cell reaches a
// sibling's http contract ONLY through it.
// A cell calling DoContract directly (with a hand-built request) bypasses the
// generated client — and would NOT trip CELL-SYNC-TRANSPORT-FUNNEL-01, whose scan
// bans raw net/http clients, not DoContract calls (a cell legitimately holds an
// injected transport.CellTransport to forward into NewClient).
//
// ## AI-robust rating: Medium — second backstop of the closed sync-transport funnel
//
// The sync-transport funnel is a closed funnel with one Hard layer and two Medium
// backstops:
//   - Upstream Hard: InProcessTransport / RemoteHTTPTransport are sealed types
//     (unexported fields + sole constructors, INPROCESS-TRANSPORT-SEALED-01 /
//     REMOTE-TRANSPORT-SEALED-01) — a transport cannot be forged.
//   - Downstream Hard: the codegen-generated contract client — Hard via codegen +
//     byte golden (the artifact is frozen), NOT interface sealing.
//     transport.CellTransport is a plain exported interface, so the constructor
//     taking it is not a type-level seal; a bare *http.Client is merely not accepted
//     by that constructor's signature.
//   - Downstream Medium backstop A (CELL-SYNC-TRANSPORT-FUNNEL-01): a cell may not
//     hold/construct a raw net/http client.
//   - Downstream Medium backstop B (THIS rule): a cell may not call DoContract
//     directly, so the generated client is the only expressible dispatch path.
//
// A + B together ⇒ the only expressible cell→sibling sync path is a generated client
// (Hard via codegen + byte golden, not interface sealing). Medium is the ceiling
// here (per ai-robust.md): DoContract is
// an exported method on an exported interface; we cannot make "calling it" a
// compile error without unexporting the seam (which the generated client, living
// in another module, must call). A typed method-call scan via ResolveMethodCall is
// the reachable ceiling for the "who may call an exported method" carrier class —
// a deliberately accepted permanent ceiling, not a deferred TODO; no fake
// Hard-upgrade issue is opened. Mirrors COMMAND-ASYNC-EMIT-CALLER-01 (#2059) and
// the CrossCellObs / EventTransportKind minter funnels (#1282/#851/#893).
//
// ## Blind spots and reverse self-checks
//
//   - Vacuity: after the #2093 configclient migration zero production cell calls
//     DoContract directly — the only former caller now dispatches via the
//     generated get.Client. The production scan (TestCellTransportDoContractCaller01)
//     is therefore vacuously green. Anti-vacuity is provided by the synthetic
//     fixture (TestCellTransportDoContractCaller01_FixtureScanRED) which MUST fire
//     on the fixture's direct ct.DoContract call, and by the pure-detector table
//     (TestCellTransportDoContractCaller01_SyntheticDetector). The production scan
//     becomes non-vacuous once a second cell adopts a generated contract client and
//     a regression re-introduces a hand-written DoContract call in a cell package;
//     until then the fixture + detector tests carry the reachability proof.
//   - A cell HOLDING a transport.CellTransport (e.g. forwarding it into
//     getv1.NewClient) is NOT flagged — only the DoContract CALL is. This is by
//     design: the generated client's constructor needs the sealed transport, and
//     forwarding it is the sanctioned wiring shape.
//   - Generated clients (generated/contracts/**) call DoContract legitimately; they
//     are not cell-owned packages, so the cell-scoped production scan never reaches
//     them (no allowlist needed).
//
// ref: tools/archtest/cell_sync_transport_funnel_test.go (sibling backstop A)
// ref: tools/archtest/internal/synctransportfixture/fixture.go (shared RED fixture)
package archtest

import (
	"testing"

	"github.com/stretchr/testify/assert"

	kerneldepgraph "github.com/ghbvf/gocell/framework/kernel/depgraph"
)

// TestCellTransportDoContractCaller01 is the production scan: no cell-owned
// production package may call transport.CellTransport.DoContract directly.
// Vacuously green after the #2093 migration — see the godoc "Vacuity" note.
func TestCellTransportDoContractCaller01(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)
	cls := kerneldepgraph.NewClassifier(moduleImportPaths(findWorkspaceModules(t, root)))

	var diags []Diagnostic
	_ = Run(t, Production(TypedOpts{Tests: false, Tags: FlatNonDefaultTags()}),
		func(p *Pass) []Diagnostic {
			if p.Pkg == nil || p.TypesInfo == nil {
				return nil
			}
			if cellOf(cls, p.Pkg.Path()) == "" {
				return nil // only cell-owned packages are in scope
			}
			for _, f := range p.Files {
				diags = append(diags, scanCellDoContractCall(p, f, p.Rel(f))...)
			}
			return nil
		})

	Report(t, ruleCellTransportDoContractCaller, diags)
}

// TestCellTransportDoContractCaller01_SyntheticDetector pins the pure
// forbiddenDoContractRef classifier (RED = transport.DoContract; GREEN = other
// methods / other packages), proving the detector branch is reachable independent
// of the repo's vacuous production state.
func TestCellTransportDoContractCaller01_SyntheticDetector(t *testing.T) {
	t.Parallel()

	assert.True(t, forbiddenDoContractRef(doContractTransportPkgPath, "DoContract"),
		"transport.CellTransport.DoContract must be forbidden for cell callers")

	green := []struct{ pkg, name string }{
		{doContractTransportPkgPath, "Bind"},                          // other transport method
		{doContractTransportPkgPath, "Resolve"},                       // other transport method
		{PlatformModulePath + "/corecells/accesscore", "DoContract"},  // same name, different package
		{"net/http", "Do"},                                            // http.Client.Do, not transport
		{PlatformFrameworkModulePath + "/runtime/auth", "DoContract"}, // same name, wrong package
	}
	for _, tc := range green {
		assert.Falsef(t, forbiddenDoContractRef(tc.pkg, tc.name),
			"%s.%s must NOT be forbidden", tc.pkg, tc.name)
	}
}

// TestCellTransportDoContractCaller01_FixtureScanRED runs the real scanner over the
// shared archtest_fixture and asserts it fires for the direct ct.DoContract call
// (fixture goodDispatch) while non-DoContract method calls (c.Do on *http.Client)
// do NOT fire — proving the method-resolution scan is load-bearing (anti-vacuity).
// The production rule runs this SAME scanCellDoContractCall.
//
// NOTE: the fixture labels that ct.DoContract line "GREEN" for backstop A
// (CELL-SYNC-TRANSPORT-FUNNEL-01's net/http symbol scan does not flag it). Under
// backstop B a DIRECT DoContract call is the RED form — the two rules read the
// same line through different detectors, which is exactly the semantic split #2093
// introduces (direct DoContract was sanctioned in US4, is now reserved for
// generated clients).
func TestCellTransportDoContractCaller01_FixtureScanRED(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{syncTransportFixturePkg}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			var d []Diagnostic
			for _, f := range p.Files {
				d = append(d, scanCellDoContractCall(p, f, p.Rel(f))...)
			}
			return d
		})

	assert.NotEmpty(t, diags,
		"CELL-TRANSPORT-DOCONTRACT-CALLER-01 RED fixture must produce violations (anti-vacuity); a miss means "+
			"ResolveMethodCall or the forbidden-method branch regressed")
	for _, d := range diags {
		assert.Contains(t, d.Rel, "synctransportfixture",
			"RED fixture diagnostic must point at the fixture, got %q", d.Rel)
		assert.Contains(t, d.Message, ruleCellTransportDoContractCaller,
			"RED fixture diagnostic must carry the rule ID")
	}
	// Exactly one direct DoContract call in the fixture (goodDispatch's
	// ct.DoContract). The *http.Client.Do calls (c.Do, http.DefaultClient.Do) are
	// method calls but NOT transport.DoContract, so they must produce no
	// diagnostics — proving no false positives on unrelated method names.
	assert.Len(t, diags, 1,
		"exactly the one direct transport.DoContract call must fire; c.Do / DefaultClient.Do must not")
}
