//go:build archtest

// INVARIANT: CELL-SYNC-TRANSPORT-FUNNEL-01
//
// # CELL-SYNC-TRANSPORT-FUNNEL-01 — cell production code must not hold/construct a raw HTTP client (Medium)
//
// ## Rule
//
// A cell's production code MUST NOT hold or construct a raw net/http client to
// dial a sibling cell. Concretely, no package owned by a cell
// (Classifier.Cell(pkg) != "") may, in a non-test production file, reference any
// of: net/http.Client (the client type), net/http.DefaultClient (the shared
// client var), or net/http.Get / .Post / .Head / .PostForm (the convenience funcs
// that dispatch via DefaultClient).
//
// Rationale (epic #1423 US4 — sync transport seam): cells communicate only
// through contracts. A cell reaches an EXTERNAL system through adapters/ (the only
// layer that may hold an *http.Client), never directly. So a cell holding or
// constructing a raw HTTP client in-process can only be dialing a SIBLING cell —
// which MUST go through an injected transport.CellTransport (DoContract) so the
// composition root can choose in-process short-circuit vs remote by topology.
// Building a request (http.NewRequestWithContext) and the server surface
// (http.Handler, http.ResponseWriter, http.Request, http.Method*, http.Status*)
// are legitimate and NOT flagged — only the CLIENT/dispatch surface is forbidden.
//
// ## AI-robust rating: Medium — closed funnel (upstream Hard + downstream Medium)
//
// This is the DOWNSTREAM half of the sync-transport funnel; the UPSTREAM half is
// Hard (InProcessTransport is a sealed type — unexported fields + sole
// constructor, INPROCESS-TRANSPORT-SEALED-01 — so a transport cannot be forged).
// Together: you cannot forge a transport (Hard upstream) AND you cannot bypass it
// with a raw http client in a cell (this Medium escape-hatch scan) = a closed
// funnel, NOT the charter's forbidden "callsite-only" shape (which lacks an
// upstream seal).
//
// Why Medium is the ceiling here (per ai-robust.md): the forbidden symbols are a
// third-party (stdlib) package's exported API; we cannot seal their construction
// nor make "referencing them" a compile error. depguard is symbol-blind (cells
// legitimately import net/http for handlers/requests), so it cannot separate the
// client surface from the handler surface. A typed callsite/symbol scan via
// ResolvePackageRef is the reachable ceiling for the "who may reference a
// third-party exported symbol" carrier class. The downstream HARD path — a
// codegen-generated contract client as the SOLE sealed sibling-call type — is
// deferred with the contract-client codegen (tracked in #2093); when it lands
// this Medium becomes its backstop.
//
// ## Blind spots and reverse self-checks
//
//   - Vacuity: after the configclient migration (US4 #1963) zero production cell
//     holds a raw http client today — the only former caller now dispatches via
//     transport.CellTransport. The production scan (TestCellSyncTransportFunnel01)
//     is therefore vacuously green — exactly like GRPC-CELL-NO-CLIENT-DIAL-01.
//     Anti-vacuity is provided by the synthetic fixture
//     (TestCellSyncTransportFunnel01_FixtureScanRED) which MUST fire, and by the
//     pure-detector table (TestCellSyncTransportFunnel01_SyntheticDetector).
//   - Method-call on a held client (`c.Do(req)`): not matched directly (Do is a
//     method on a value, not a package selector), but holding/constructing the
//     *http.Client to call it on IS matched (the client-type reference), so the
//     escape is closed at the type reference, mirroring the gRPC rule's ClientConn
//     coverage.
//   - L0 carve-out (ADR D4): an L0 cell does not make sync contract calls through
//     this seam; the rule targets http-client references, so an L0 package
//     referencing http.Client is still flagged unless it is an adapter — keeping
//     the carve-out narrow (does not relax constitution Article I).
//   - Dot-import (`import . "net/http"`): bare `Get(...)` not walked
//     (selector-only); cells cannot dot-import (revive linter is the gate).
//
// ## Symbol inventory (forbidden client symbol set)
//
//	net/http: Client                          (client-type)
//	net/http: DefaultClient                   (default-client)
//	net/http: Get, Post, Head, PostForm       (convenience — dispatch via DefaultClient)
//
// Explicitly NOT forbidden (request shaping / server surface): http.NewRequest,
// http.NewRequestWithContext, http.Handler, http.HandlerFunc, http.Request,
// http.ResponseWriter, http.Header, http.Method*, http.Status*, http.Cookie, ...
//
// ref: tools/archtest/grpc_cell_no_client_dial_test.go (sibling US8 funnel template)
// ref: tools/archtest/internal/synctransportfixture/fixture.go (RED fixture)
package archtest

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	kerneldepgraph "github.com/ghbvf/gocell/framework/kernel/depgraph"
)

// syncTransportFixturePkg is the archtest_fixture RED fixture package pattern.
const syncTransportFixturePkg = "./tools/archtest/internal/synctransportfixture"

// TestCellSyncTransportFunnel01 is the production scan: every cell-owned
// production package (Classifier.Cell != "") must be free of raw HTTP client
// references. Vacuously green today — see the godoc "Blind spots / Vacuity" note.
func TestCellSyncTransportFunnel01(t *testing.T) {
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
				diags = append(diags, scanCellHTTPClientDispatch(p, f, p.Rel(f))...)
			}
			return nil
		})

	Report(t, ruleCellSyncTransportFunnel, diags)
}

// TestCellSyncTransportFunnel01_SyntheticDetector pins the pure
// forbiddenHTTPClientRef classifier (RED forbidden set + GREEN request-shaping /
// server / non-http set), proving the detector branch is reachable independent of
// the repo's vacuous production state.
func TestCellSyncTransportFunnel01_SyntheticDetector(t *testing.T) {
	t.Parallel()

	red := []struct {
		name, wantKind string
	}{
		{"Client", "client-type"},
		{"DefaultClient", "default-client"},
		{"Get", "convenience"},
		{"Post", "convenience"},
		{"Head", "convenience"},
		{"PostForm", "convenience"},
	}
	for _, tc := range red {
		kind, forbidden := forbiddenHTTPClientRef(netHTTPLibPath, tc.name)
		assert.Truef(t, forbidden, "net/http.%s must be forbidden", tc.name)
		assert.Equalf(t, tc.wantKind, kind, "net/http.%s wrong kind", tc.name)
	}

	green := []struct{ pkg, name string }{
		{netHTTPLibPath, "NewRequestWithContext"}, // request shaping — legitimate
		{netHTTPLibPath, "NewRequest"},
		{netHTTPLibPath, "Handler"},     // server surface
		{netHTTPLibPath, "HandlerFunc"}, // server surface
		{netHTTPLibPath, "Request"},     // request type
		{netHTTPLibPath, "ResponseWriter"},
		{netHTTPLibPath, "Header"},
		{netHTTPLibPath, "MethodGet"}, // method const
		{netHTTPLibPath, "StatusOK"},  // status const
		{netHTTPLibPath, "ServeMux"},  // server mux
		// a cell's own type named Client, or a transport package, must NOT fire.
		{PlatformModulePath + "/corecells/accesscore", "Client"},
		{PlatformFrameworkModulePath + "/runtime/transport", "CellTransport"},
	}
	for _, tc := range green {
		_, forbidden := forbiddenHTTPClientRef(tc.pkg, tc.name)
		assert.Falsef(t, forbidden, "%s.%s must NOT be forbidden", tc.pkg, tc.name)
	}
}

// TestCellSyncTransportFunnel01_FixtureScanRED runs the real scanner over the
// archtest_fixture RED fixture and asserts it fires for the forbidden client
// forms while the GREEN anchors (transport.DoContract + request shaping) do NOT
// fire — proving the scan layer (SelectorExpr walk + ResolvePackageRef) is
// load-bearing and that deleting the violating lines would turn it green
// (anti-vacuity). The production rule runs this SAME scanCellHTTPClientDispatch.
func TestCellSyncTransportFunnel01_FixtureScanRED(t *testing.T) {
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
				d = append(d, scanCellHTTPClientDispatch(p, f, p.Rel(f))...)
			}
			return d
		})

	assert.NotEmpty(t, diags,
		"CELL-SYNC-TRANSPORT-FUNNEL-01 RED fixture must produce violations (anti-vacuity); a miss means "+
			"the resolver or the forbidden-symbol branch regressed")

	kinds := map[string]int{}
	for _, d := range diags {
		assert.Contains(t, d.Rel, "synctransportfixture",
			"RED fixture diagnostic must point at the fixture, got %q", d.Rel)
		assert.Contains(t, d.Message, ruleCellSyncTransportFunnel,
			"RED fixture diagnostic must carry the rule ID")
		for _, k := range []string{"client-type", "default-client", "convenience"} {
			if strings.Contains(d.Message, "["+k+"]") {
				kinds[k]++
			}
		}
		// GREEN anchor — request shaping must never be flagged. (Checked via the
		// FLAGGED-symbol phrasing "references net/http.NewRequestWithContext"; the
		// rule's guidance text mentions DoContract by name, so a bare substring
		// check would false-fail — the count assertion below proves the GREEN
		// goodDispatch via DoContract produced zero diagnostics.)
		assert.NotContains(t, d.Message, "references net/http.NewRequestWithContext",
			"http.NewRequestWithContext (request shaping) must NOT be flagged")
	}
	assert.Positive(t, kinds["client-type"], "raw *http.Client construction/hold must fire")
	assert.Positive(t, kinds["convenience"], "http.Get/Post convenience dispatch must fire")
	assert.Positive(t, kinds["default-client"], "http.DefaultClient reference must fire")
	// Exactly the four forbidden references fire (fixture.go: &http.Client{},
	// *http.Client field, http.Get, http.DefaultClient). The GREEN goodDispatch
	// (transport.DoContract + http.NewRequestWithContext request shaping) and the
	// non-forbidden http.MethodGet / http.NewRequestWithContext references produce
	// none — proving no false positives.
	assert.Len(t, diags, 4,
		"exactly the 4 forbidden raw-HTTP-client references must fire; the sanctioned DoContract path and "+
			"request shaping must produce no diagnostics")
}
