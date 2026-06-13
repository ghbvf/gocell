//go:build archtest

// INVARIANT: GRPC-CELL-NO-CLIENT-DIAL-01
//
// # GRPC-CELL-NO-CLIENT-DIAL-01 — cell production code must not construct or hold a gRPC client (Medium)
//
// ## Rule
//
// A cell's production code MUST NOT construct or hold a gRPC client connection.
// Concretely, no package owned by a cell (Classifier.Cell(pkg) != "") may, in a
// non-test production file, reference any of:
//
//   - google.golang.org/grpc.Dial / .DialContext / .NewClient   (dial primitive)
//   - google.golang.org/grpc.ClientConn / .ClientConnInterface   (client-conn type)
//   - ANY generated gRPC CLIENT surface symbol — the New<Svc>Client constructor,
//     the <Svc>Client interface, or the <Svc>_<Method>Client stream alias (every
//     client-surface symbol is suffixed "Client") — in a package under
//     <PlatformModulePath>/generated/contracts/grpc/...           (generated-client)
//
// Rationale (epic #1423 / US8 — 进程内跨 cell Go 直传 = 0): cells communicate
// only through contracts. A cell reaches an EXTERNAL system through adapters/
// (the only layer that may dial), never directly. So a cell constructing a gRPC
// ClientConn in-process can only be dialing a SIBLING cell. But construction is
// not the only escape: a cell that merely HOLDS a generated <Svc>Client interface
// (a composition root injects a built client, the cell keeps the interface and
// calls it) reaches the sibling with no ClientConn / New*Client in sight — so the
// whole generated client surface is forbidden, not just its constructor (#1961
// F1). Server-side registration (grpc.ServiceRegistrar, the generated <Svc>Server
// interface / Register<Svc>Server — suffixed "Server") is legitimate, NOT flagged.
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
//   - Generated client surface coverage: the constructor, the <Svc>Client
//     interface, AND the <Svc>_<Method>Client stream alias (all suffixed
//     "Client") have REAL fixture-AST coverage — grpccelldialfixture imports the
//     actual generated module (tools/go.mod require+replace
//     github.com/ghbvf/gocell/generated; archtest lives in the tools/ module,
//     NOT the main module — an earlier draft wrongly claimed a fixture could not
//     import it, #1961 F2) and both constructs a client and holds the interface
//     (#1961 F1). The server surface (suffixed "Server") stays GREEN, also
//     exercised by the fixture.
//   - Suffix heuristic edge: a generated proto MESSAGE type named to end in
//     "Client" would false-RED. protoc-gen-go derives message names from the
//     .proto and never appends a Client/Server role suffix, so this cannot arise
//     from the generated contract pipeline; noted for honesty, not separately gated.
//   - A cell naming google.golang.org/grpc.ServerStreamingClient (the raw stream
//     client type) directly — rather than the generated <Svc>_<Method>Client
//     alias — is NOT flagged (the grpc-lib branch locks only Dial/NewClient/
//     ClientConn). A cell is the SERVER side (it implements the service), so it
//     uses ServerStreamingServer, not the client type; the generated alias path
//     covers the realistic case.
//   - Function-value indirection (`f := grpc.NewClient; f(...)`): the bare-ident
//     `grpc.NewClient` selector is still walked and resolved, so assigning the
//     method value is itself flagged.
//   - Dot-import (`import . "google.golang.org/grpc"`): bare `NewClient(...)` is
//     NOT walked by scanGRPCClientConstruction (it walks qualified selectors
//     only). Cells cannot dot-import (the revive dot-imports linter is the gate),
//     so this form cannot arise in a cell — mirrors GRPC-SERVICE-IN-CONTRACT-01
//     blind spot B3 (the linter, not the scanner, is the defense).
//
// ## Symbol inventory (forbidden client symbol set)
//
//	google.golang.org/grpc: Dial, DialContext, NewClient               (dial)
//	google.golang.org/grpc: ClientConn, ClientConnInterface            (conn-type)
//	<PlatformModulePath>/generated/contracts/grpc/...: "Client" suffix  (generated-client)
//	  — New<Svc>Client ctor, <Svc>Client interface, <Svc>_<Method>Client stream alias
//
// Explicitly NOT forbidden (server side / message types):
// grpc.ServiceRegistrar, grpc.NewServer, grpc.ServiceDesc, grpc.ServerStream,
// the generated <Svc>Server interface, Register<Svc>Server, Unimplemented<Svc>Server,
// Unsafe<Svc>Server, <Svc>_<Method>Server (all suffixed "Server"), message types.
//
// ref: tools/archtest/grpc_service_in_contract_test.go (typed callsite scan + synthetic fixture template)
// ref: tools/archtest/svctoken_caller_cell.go (ResolvePackageRef callsite identification)
// ref: tools/archtest/internal/grpccelldialfixture/fixture.go (RED fixture)
package archtest

import (
	"fmt"
	"go/ast"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	kerneldepgraph "github.com/ghbvf/gocell/kernel/depgraph"
)

// ruleGRPCCellNoClientDial is the archtest rule identifier.
const ruleGRPCCellNoClientDial = "GRPC-CELL-NO-CLIENT-DIAL-01"

// grpcRuntimeLibPath is the canonical import path of the gRPC transport library.
const grpcRuntimeLibPath = "google.golang.org/grpc"

// generatedGRPCContractsMarker is the import-path PREFIX of the buf/protoc
// generated gRPC contract packages — the separate module
// github.com/ghbvf/gocell/generated, anchored at <PlatformModulePath>/generated.
// Matched by prefix (isGeneratedGRPCPkg), so a vendored/stray package whose path
// merely contains the segment mid-string cannot false-match (#1961 F3).
const generatedGRPCContractsMarker = PlatformModulePath + "/generated/contracts/grpc/"

// grpcCellDialFixturePkg is the archtest_fixture RED fixture package pattern.
const grpcCellDialFixturePkg = "./tools/archtest/internal/grpccelldialfixture"

// isGeneratedGRPCPkg reports whether pkgPath is a buf/protoc generated gRPC
// contract package, by import-path PREFIX (not a mid-string substring — #1961 F3).
func isGeneratedGRPCPkg(pkgPath string) bool {
	return strings.HasPrefix(pkgPath, generatedGRPCContractsMarker)
}

// forbiddenGRPCClientRef classifies a (pkgPath, name) package-symbol reference
// as a forbidden cross-cell gRPC client-construction-or-surface symbol. It
// returns the violation kind ("dial" / "conn-type" / "generated-client") and
// true when forbidden, or ("", false) otherwise. Server-side symbols
// (grpc.ServiceRegistrar, grpc.NewServer, the generated <Svc>Server /
// Register<Svc>Server / Unimplemented... / Unsafe...) and any non-grpc symbol
// are not forbidden. Pure detector core shared by the production scan and the
// synthetic table (mirrors detectOrphanRegs in grpc_service_in_contract).
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
	// In a generated gRPC contract package the entire CLIENT surface is suffixed
	// "Client": the New<Svc>Client constructor, the <Svc>Client interface, AND
	// the <Svc>_<Method>Client stream alias. A cell referencing ANY of them can
	// hold/inject a sibling-cell transport client and call it directly — the
	// injection-then-call escape the construction-only check missed (#1961 F1).
	// The SERVER surface (all suffixed "Server") is legitimate registration.
	if isGeneratedGRPCPkg(pkgPath) && strings.HasSuffix(name, "Client") {
		return "generated-client", true
	}
	return "", false
}

// scanGRPCClientConstruction walks one file's AST for qualified selector
// references (`pkg.Name`) and emits a Diagnostic for every forbidden gRPC
// client-construction symbol. The caller decides whether the file belongs to a
// cell (production scan) or not (fixture scan). Dot-imported bare `Name` is NOT
// walked here (selector-only) — cells cannot dot-import (revive dot-imports
// linter is the gate), so that form is an independent linter concern, not a
// scanner one (see the godoc blind-spots section).
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
				"%s [%s]: cell production code references %s.%s at %s:%d — a cell must not construct OR hold a "+
					"gRPC client (dial primitive / *grpc.ClientConn / generated <Svc>Client surface: interface, "+
					"New*Client, or stream client). In-process cross-cell calls go through contracts, not a direct "+
					"gRPC client; external gRPC reach belongs in adapters/, never a cell (epic #1423 US8: 进程内跨 cell Go 直传 = 0).",
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
		// generated CLIENT surface — all suffixed "Client" (#1961 F1): the
		// constructor, the interface (the injection-then-call escape), and the
		// stream-client alias. Referencing ANY lets a cell call a sibling cell.
		{genPkg, "NewDeviceCommandServiceClient", "generated-client"},            // constructor
		{genPkg, "DeviceCommandServiceClient", "generated-client"},               // interface (F1 injection path)
		{genPkg, "DeviceCommandService_WatchCommandsClient", "generated-client"}, // stream-client alias
		{genPkg, "NewFooClient", "generated-client"},
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
		// generated SERVER surface — all suffixed "Server" → legitimate registration.
		{genPkg, "DeviceCommandServiceServer"},                        // server interface
		{genPkg, "RegisterDeviceCommandServiceServer"},                // server registrar
		{genPkg, "UnimplementedDeviceCommandServiceServer"},           // forward-compat base
		{genPkg, "UnsafeDeviceCommandServiceServer"},                  // opt-out marker
		{genPkg, "DeviceCommandService_WatchCommandsServer"},          // stream-server alias
		{genPkg, "IssueCommandRequest"},                               // a message type (neither Client nor Server)
		{PlatformModulePath + "/runtime/grpc", "NewServiceRegistrar"}, // runtime/grpc, not the lib
		{PlatformModulePath + "/corecells/accesscore", "NewClient"},   // a cell's own *Client, not lib/generated
		// #1961 F3 negative: a VENDORED generated path — the marker appears
		// mid-string but NOT as a prefix, so HasPrefix (not Contains) must reject it.
		{"example.com/vendor/" + PlatformModulePath + "/generated/contracts/grpc/device/command/v1", "NewDeviceCommandServiceClient"},
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
		for _, k := range []string{"dial", "conn-type", "generated-client"} {
			if strings.Contains(d.Message, "["+k+"]") {
				kinds[k]++
			}
		}
		// GREEN anchors — neither the grpc server-registration seam
		// (grpc.ServiceRegistrar) nor the generated SERVER surface
		// (DeviceCommandServiceServer / RegisterDeviceCommandServiceServer) may
		// ever appear in a diagnostic.
		assert.NotContains(t, d.Message, "ServiceRegistrar",
			"grpc.ServiceRegistrar (server seam) must NOT fire")
		assert.NotContains(t, d.Message, "ServiceServer",
			"generated server surface (*ServiceServer / Register*ServiceServer) must NOT fire")
	}
	assert.Positive(t, kinds["dial"], "dial primitive (grpc.NewClient) must fire")
	assert.Positive(t, kinds["conn-type"], "client-conn type (*grpc.ClientConn / ClientConnInterface) must fire")
	assert.Positive(t, kinds["generated-client"],
		"generated client surface (New*Client ctor + <Svc>Client interface) must fire")
	// The stub branch (generated New*Client) cannot fire from this fixture: it
	// imports only google.golang.org/grpc, NOT the separate generated module. The
	// stub detector branch is pinned by TestGRPCCellNoClientDial01_SyntheticDetector.
	// Asserting zero here guards against a future fixture edit silently relying on
	// (and masking a regression in) the unexercised stub path.
	assert.Zero(t, kinds["stub"], "stub branch must NOT fire from this grpc-only fixture")
}
