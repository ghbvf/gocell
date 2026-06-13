//go:build archtest

// INVARIANT: GRPC-CELL-NO-CLIENT-DIAL-01
//
// # GRPC-CELL-NO-CLIENT-DIAL-01 — cell production code must not construct a gRPC client (Medium)
//
// ## Rule
//
// A cell's production code MUST NOT construct or hold a gRPC client connection.
// Concretely, no package owned by a cell (Classifier.Cell(pkg) != "") may, in a
// non-test production file, reference any of:
//
//   - google.golang.org/grpc.Dial / .DialContext / .NewClient   (dial primitive)
//   - google.golang.org/grpc.ClientConn / .ClientConnInterface   (client-conn type)
//   - a generated gRPC client constructor New<Svc>Client whose package lives
//     under .../generated/contracts/grpc/...                     (client stub)
//
// Rationale (epic #1423 / US8 — 进程内跨 cell Go 直传 = 0): cells communicate
// only through contracts. A cell reaches an EXTERNAL system through adapters/
// (the only layer that may dial), never directly. Therefore the only thing a
// cell could be doing by constructing a gRPC ClientConn in-process is dialing a
// SIBLING cell's gRPC service — the cross-cell direct connection this invariant
// forbids. Server-side registration (naming grpc.ServiceRegistrar, calling the
// generated Register<Svc>Server) is legitimate and explicitly NOT flagged.
//
// Why not depguard (the gap #1961 names): .golangci.yml cells-isolation must
// ALLOW cells to import google.golang.org/grpc, because the cellgen-generated
// cell_gen.go Register callback `func(r grpc.ServiceRegistrar){ ... }` names the
// transport lib (#1601). depguard is symbol-blind at import granularity, so it
// cannot distinguish a cell legitimately importing grpc for server registration
// from a cell illegitimately calling grpc.NewClient to dial a sibling. This rule
// closes that gap at callsite/symbol granularity.
//
// ## AI-robust rating: Medium — and why Hard is unreachable here (per ai-robust.md)
//
// This is a caller-allowlist constraint ("a cell must not CALL grpc.NewClient").
// Each Hard carrier is unreachable without the already-rejected #1582 marker
// rework (and that rework is the SERVER path, out of scope here):
//
//  1. type system / sealed construction — grpc.ClientConn / grpc.NewClient are a
//     third-party package's exported API; we cannot seal their construction nor
//     make "calling them" a compile error. Any package may import & call them. ✗
//  2. codegen funnel + golden — a client dial is hand-written business code; it
//     flows through no codegen seam, so there is no golden to byte-lock. ✗
//  3. reflect field freeze — this forbids calling a function, not the shape of a
//     struct/interface; nothing to freeze. ✗
//  4. depguard import-deny (the closest-to-Hard depth) — a cell's only
//     production grpc reference today is the generated cell_gen.go, so depguard
//     could in principle deny grpc imports in a cell's non-cell_gen.go files,
//     making `grpc.NewClient` unnameable in hand code. NOT adopted: depguard is
//     symbol-blind (cannot separate client vs server use of the same grpc pkg),
//     it leans on the fragile "only cell_gen.go names grpc" premise (a future
//     generated streaming surface that names grpc in a hand handler breaks it),
//     and it still cannot match the symbol precision needed for the generated
//     stub / conn-injection forms.
//
// Conclusion: this constraint is structurally identical to
// GRPC-SERVICE-IN-CONTRACT-01/A (who may call reg.GRPCService) and
// SVCTOKEN-CALLER-CELL-REQUIRED-01 (who may call GenerateServiceToken). Medium
// (typed callsite scan via ResolvePackageRef) is the reachable ceiling for the
// "who may call a third-party exported function" carrier class; there is NO
// low-cost Hard path. Permanent ceiling, same #1631 family as
// GRPC-SERVICE-IN-CONTRACT-01/A — no Hard-conversion issue is filed (no cheap
// path exists).
//
// ## Blind spots and reverse self-checks
//
//   - Vacuity: zero production cell constructs a gRPC client today (cells only
//     register servers; the only grpc.NewClient callsites are in _test.go
//     bufnet harnesses, excluded by Tests:false). The production scan
//     (TestGRPCCellNoClientDial01) is therefore vacuously green — exactly like
//     GRPC-SERVICE-IN-CONTRACT-01. Anti-vacuity is provided by the synthetic
//     fixture (TestGRPCCellNoClientDial01_FixtureScanRED) which MUST fire, and by
//     the pure-detector table (TestGRPCCellNoClientDial01_SyntheticDetector).
//   - Generated-stub AST coverage: the stub branch (New<Svc>Client under
//     /generated/contracts/grpc/) is covered only by the pure-detector table,
//     NOT by a fixture-AST case — the generated tree is a SEPARATE go module
//     (github.com/ghbvf/gocell/generated) that the main module deliberately does
//     not depend on, so a main-module fixture cannot import it. The SCAN layer
//     (SelectorExpr walk + ResolvePackageRef → pkgPath/name) is shared with the
//     dial branch, which the fixture DOES exercise; only the detector's
//     pkgPath-prefix/name-regex branch is fixture-uncovered, and the synthetic
//     table pins it. dial is the chokepoint anyway (no ClientConn → no stub call
//     unless the conn is smuggled via interface{} — see the doc.go inventory's
//     open blind spot).
//   - Function-value indirection (`f := grpc.NewClient; f(...)`): the bare-ident
//     `grpc.NewClient` selector is still walked and resolved, so assigning the
//     method value is itself flagged.
//   - Dot-import (`import . "google.golang.org/grpc"`): bare `NewClient(...)`
//     resolves via ResolvePackageRef's *types.Func path; additionally cells
//     cannot dot-import per the revive dot-imports linter.
//
// ## Symbol inventory (forbidden client-construction symbol set)
//
//	google.golang.org/grpc: Dial, DialContext, NewClient        (dial)
//	google.golang.org/grpc: ClientConn, ClientConnInterface     (conn-type)
//	.../generated/contracts/grpc/...: ^New[A-Za-z0-9]*Client$   (stub)
//
// Explicitly NOT forbidden (server side / type references):
// grpc.ServiceRegistrar, grpc.NewServer, grpc.ServiceDesc, grpc.ServerStream,
// the generated Register<Svc>Server, the generated <Svc>Client interface type.
//
// ref: tools/archtest/grpc_service_in_contract_test.go (typed callsite scan + synthetic fixture template)
// ref: tools/archtest/svctoken_caller_cell.go (ResolvePackageRef callsite identification)
// ref: tools/archtest/internal/grpccelldialfixture/fixture.go (RED fixture)
package archtest

import (
	"fmt"
	"go/ast"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
)

// ruleGRPCCellNoClientDial is the archtest rule identifier.
const ruleGRPCCellNoClientDial = "GRPC-CELL-NO-CLIENT-DIAL-01"

// grpcRuntimeLibPath is the canonical import path of the gRPC transport library.
const grpcRuntimeLibPath = "google.golang.org/grpc"

// generatedGRPCContractsMarker is the path segment shared by every buf/protoc
// generated gRPC contract package (where the typed New<Svc>Client stub
// constructors live). It is a substring (not a prefix) so it matches regardless
// of how the separate `generated` module's path is anchored.
const generatedGRPCContractsMarker = "/generated/contracts/grpc/"

// grpcClientCtorRe matches a generated gRPC client constructor name
// (New<Svc>Client). Anchored so it never matches Register<Svc>Server or the
// bare <Svc>Client interface type.
var grpcClientCtorRe = regexp.MustCompile(`^New[A-Za-z0-9]*Client$`)

// grpcCellDialFixturePkg is the archtest_fixture RED fixture package pattern.
const grpcCellDialFixturePkg = "./tools/archtest/internal/grpccelldialfixture"

// forbiddenGRPCClientRef classifies a (pkgPath, name) package-symbol reference
// as a forbidden cross-cell gRPC client-construction symbol. It returns the
// violation kind ("dial" / "conn-type" / "stub") and true when forbidden, or
// ("", false) otherwise. Server-side symbols (grpc.ServiceRegistrar,
// grpc.NewServer, the generated Register<Svc>Server) and any non-grpc symbol are
// not forbidden. This is the pure detector core shared by the production scan
// and the synthetic table (mirrors detectOrphanRegs in grpc_service_in_contract).
func forbiddenGRPCClientRef(pkgPath, name string) (kind string, forbidden bool) {
	if pkgPath == grpcRuntimeLibPath {
		switch name {
		case "Dial", "DialContext", "NewClient":
			return "dial", true
		case "ClientConn", "ClientConnInterface":
			return "conn-type", true
		default:
			return "", false
		}
	}
	if strings.Contains(pkgPath, generatedGRPCContractsMarker) && grpcClientCtorRe.MatchString(name) {
		return "stub", true
	}
	return "", false
}

// scanGRPCClientConstruction walks one file's AST for package-symbol references
// (selector `pkg.Name` and dot-imported bare `Name`) and emits a Diagnostic for
// every forbidden gRPC client-construction symbol. The caller decides whether
// the file belongs to a cell (production scan) or not (fixture scan).
func scanGRPCClientConstruction(p *Pass, file *ast.File, rel string) []Diagnostic {
	var diags []Diagnostic
	emit := func(node ast.Expr) {
		path, name, ok := ResolvePackageRef(p.TypesInfo, node)
		if !ok {
			return
		}
		kind, forbidden := forbiddenGRPCClientRef(path, name)
		if !forbidden {
			return
		}
		line := p.Fset.Position(node.Pos()).Line
		diags = append(diags, Diagnostic{
			Rel:  rel,
			Line: line,
			Message: fmt.Sprintf(
				"%s [%s]: cell production code references %s.%s at %s:%d — a cell must not construct a "+
					"gRPC client (dial primitive / generated New*Client stub / *grpc.ClientConn). In-process "+
					"cross-cell calls go through contracts, not a direct gRPC dial; external gRPC reach belongs "+
					"in adapters/, never a cell (epic #1423 US8: 进程内跨 cell Go 直传 = 0).",
				ruleGRPCCellNoClientDial, kind, path, name, rel, line),
		})
	}
	EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) { emit(sel) })
	return diags
}

// TestGRPCCellNoClientDial01 is the production scan: every cell-owned production
// package (Classifier.Cell != "") must be free of gRPC client construction.
// Vacuously green today — see the godoc "Blind spots / Vacuity" note.
func TestGRPCCellNoClientDial01(t *testing.T) {
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
				diags = append(diags, scanGRPCClientConstruction(p, f, p.Rel(f))...)
			}
			return nil
		})

	Report(t, ruleGRPCCellNoClientDial, diags)
}

// TestGRPCCellNoClientDial01_SyntheticDetector pins the pure forbiddenGRPCClientRef
// classifier (RED forbidden set + GREEN server-side / type-reference set),
// proving the detector branch is reachable independent of the repo's vacuous
// production state.
func TestGRPCCellNoClientDial01_SyntheticDetector(t *testing.T) {
	t.Parallel()

	const genPkg = PlatformModulePath + "/generated/contracts/grpc/device/command/v1"

	red := []struct {
		pkg, name, wantKind string
	}{
		{grpcRuntimeLibPath, "Dial", "dial"},
		{grpcRuntimeLibPath, "DialContext", "dial"},
		{grpcRuntimeLibPath, "NewClient", "dial"},
		{grpcRuntimeLibPath, "ClientConn", "conn-type"},
		{grpcRuntimeLibPath, "ClientConnInterface", "conn-type"},
		{genPkg, "NewDeviceCommandServiceClient", "stub"},
		{genPkg, "NewFooClient", "stub"},
	}
	for _, tc := range red {
		kind, forbidden := forbiddenGRPCClientRef(tc.pkg, tc.name)
		assert.Truef(t, forbidden, "%s.%s must be forbidden", tc.pkg, tc.name)
		assert.Equalf(t, tc.wantKind, kind, "%s.%s wrong kind", tc.pkg, tc.name)
	}

	green := []struct{ pkg, name string }{
		{grpcRuntimeLibPath, "ServiceRegistrar"}, // server registration param type
		{grpcRuntimeLibPath, "NewServer"},        // server construction, not client
		{grpcRuntimeLibPath, "ServiceDesc"},
		{grpcRuntimeLibPath, "ServerStream"},
		{genPkg, "RegisterDeviceCommandServiceServer"}, // generated server registrar
		{genPkg, "DeviceCommandServiceClient"},         // the client interface TYPE (not its New ctor)
		{genPkg, "FooServerClient"},                    // ends in Client but no New prefix
		{PlatformModulePath + "/runtime/grpc", "NewServiceRegistrar"}, // runtime/grpc, not the lib
		{PlatformModulePath + "/corecells/accesscore", "NewClient"},   // a cell's own NewClient, not grpc
	}
	for _, tc := range green {
		_, forbidden := forbiddenGRPCClientRef(tc.pkg, tc.name)
		assert.Falsef(t, forbidden, "%s.%s must NOT be forbidden", tc.pkg, tc.name)
	}
}

// TestGRPCCellNoClientDial01_FixtureScanRED runs the real scanner over the
// archtest_fixture RED fixture and asserts it fires for the dial + conn-type
// forms while the server-registration GREEN anchor (grpc.ServiceRegistrar) does
// NOT fire — proving the scan layer (SelectorExpr walk + ResolvePackageRef) is
// load-bearing and that deleting the violating lines would turn it green
// (anti-vacuity). The production rule runs this SAME scanGRPCClientConstruction.
func TestGRPCCellNoClientDial01_FixtureScanRED(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	diags := Run(t, Fixture(FixtureOpts{Tests: false}, []string{grpcCellDialFixturePkg}),
		func(p *Pass) []Diagnostic {
			if !p.Typed() {
				return nil
			}
			var d []Diagnostic
			for _, f := range p.Files {
				d = append(d, scanGRPCClientConstruction(p, f, p.Rel(f))...)
			}
			return d
		})

	assert.NotEmpty(t, diags,
		"GRPC-CELL-NO-CLIENT-DIAL-01 RED fixture must produce violations (anti-vacuity); a miss means "+
			"the resolver or the forbidden-symbol branch regressed")

	kinds := map[string]int{}
	for _, d := range diags {
		assert.Contains(t, d.Rel, "grpccelldialfixture",
			"RED fixture diagnostic must point at the fixture, got %q", d.Rel)
		assert.Contains(t, d.Message, ruleGRPCCellNoClientDial,
			"RED fixture diagnostic must carry the rule ID")
		for _, k := range []string{"dial", "conn-type", "stub"} {
			if strings.Contains(d.Message, "["+k+"]") {
				kinds[k]++
			}
		}
		// The GREEN anchor names grpc.ServiceRegistrar; no forbidden ref does, so
		// no diagnostic may ever mention it.
		assert.NotContains(t, d.Message, "ServiceRegistrar",
			"server registration (grpc.ServiceRegistrar) must NOT fire")
	}
	assert.Positive(t, kinds["dial"], "dial primitive (grpc.NewClient) must fire")
	assert.Positive(t, kinds["conn-type"], "client-conn type (*grpc.ClientConn / ClientConnInterface) must fire")
}
