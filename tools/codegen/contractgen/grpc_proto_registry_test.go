// INVARIANT: GRPC-PROTO-REGISTRY-SINGLE-SOURCE-01
//
// The proto import path + request/response message type names emitted into a
// grpc stub's iface_gen.go MUST come from the .proto file (resolved by the
// ProtoRegistry / readProtoTypeInfo), never from a hand-written literal in
// iface.tmpl or a divergent field write. Carriers (downstream Hard + supporting
// Medium + a tracked Go ceiling):
//
//   C2 (Hard, regenerate-and-diff byte-lock) — TestRender_Golden_Synth_GRPC
//      (render_test.go) byte-locks the rendered stub through the production
//      render funnel; `-update` regenerates through the same funnel. This is the
//      "codegen funnel + golden" Hard form (.claude/rules/gocell/ai-robust.md).
//
//   C3 (Hard, render-compare to an INDEPENDENT proto oracle) —
//      TestGRPC_PROTO_REGISTRY_SINGLE_SOURCE_01_ImportMatchesProtoOracle renders
//      the stub fresh, AST-walks its imports, and asserts the single non-context
//      import byte-equals readProtoTypeInfo(proto).ImportPath, and that the
//      method signature references the oracle's alias.RequestType / .ResponseType.
//      The oracle is computed straight from the .proto, NOT from spec.GRPC, so a
//      template that hard-codes a divergent import OR a builder that fills the
//      field from the wrong source both fail here. Non-vacuous every run;
//      survives `go test -update` (regenerating a corrupted template still diffs
//      the emitted import against the proto-derived oracle). Same robustness
//      shape as the deleted GRPC-CODEGEN-NO-PROTO-DEP-01.
//
//   C1 (Medium, single sanctioned constructor — AST) —
//      TestGRPC_PROTO_REGISTRY_SINGLE_SOURCE_01_SpecConstructedOnlyInBuilder
//      asserts every GRPCEndpointSpec composite literal in production code is
//      lexically inside buildGRPCSpec, so the proto-derived fields have exactly
//      one write site to review. AST name-anchored (not go/types); the package
//      has a single GRPCEndpointSpec type so the Ident match is unambiguous.
//
//   C4 (Medium, collision uniqueness) —
//      TestGRPC_PROTO_REGISTRY_SINGLE_SOURCE_01_Collision* feed two synthesized
//      specs and assert protoRegistry.register rejects a duplicate
//      (proto-package, service, method) and a divergent import for the same
//      service. Non-vacuous despite the single synth fixture (which alone never
//      collides).
//
// Funnel two-way rating: downstream Hard (C2 + C3) + supporting Medium (C1, C4).
//
// Go ceiling (won't-do, tracked at gh #1518): in the SINGLE-grpc-contract window
// (PR 6 ships exactly one synth fixture; first real contract is PR 8), a literal
// in iface.tmpl that EXACTLY equals the registry's current value would pass C2
// and C3 because there is no second contract to diverge from. Go cannot
// type-check the string a text/template emits, so an upstream type-system seal
// is structurally impossible — same permanent ceiling as SPAN-SETATTR-HOLDER-SEAL
// (#851) / HEALTHZ-HOLDER-SEAL (#893) / outbox principal-write (#1282). C3 closes
// it the moment a second grpc contract exists (its independent per-contract
// oracle diverges from the shared literal). PR body records this partial-vacuity
// ("no silent caps").
//
// Blind spots of the chosen tools + reverse self-checks:
//   - go/parser ImportsOnly walk (C3): a renamed alias or divergent path still
//     carries the path in ImportSpec.Path; _ImportOracleDetectorIsLive feeds an
//     iface whose proto import diverges and asserts it IS flagged (clean passes).
//   - AST Ident match (C1): a second GRPCEndpointSpec composite literal in a
//     non-builder function is flagged by _SpecConstructorDetectorIsLive.
package contractgen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

const grpcSingleSourceContractID = "grpc.device.command.v1"

// --- C3: rendered import + type names match the independent proto oracle ------

func TestGRPC_PROTO_REGISTRY_SINGLE_SOURCE_01_ImportMatchesProtoOracle(t *testing.T) {
	t.Parallel()
	oracle, err := readProtoTypeInfo(fixtureProtoPath(t), "IssueCommand")
	if err != nil {
		t.Fatalf("oracle readProtoTypeInfo: %v", err)
	}

	testDir, err := filepath.Abs(filepath.Join("testdata", "synth", "synth_grpc_minimal"))
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	p, err := metadata.NewParser(testDir).Parse()
	if err != nil {
		t.Fatalf("parse synth_grpc_minimal: %v", err)
	}
	spec, err := buildContractSpec(testDir, p, grpcSingleSourceContractID)
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
	}
	src := renderFile(t, spec, "iface_gen.go")

	imports := nonContextImports(t, src)
	if len(imports) != 1 {
		t.Fatalf("expected exactly one non-context import, got %v", imports)
	}
	if imports[0] != oracle.ImportPath {
		t.Errorf("GRPC-PROTO-REGISTRY-SINGLE-SOURCE-01: rendered proto import %q != proto oracle %q",
			imports[0], oracle.ImportPath)
	}

	wantReq := "*" + oracle.Alias + "." + oracle.RequestType
	wantResp := "*" + oracle.Alias + "." + oracle.ResponseType
	if !strings.Contains(string(src), wantReq) || !strings.Contains(string(src), wantResp) {
		t.Errorf("GRPC-PROTO-REGISTRY-SINGLE-SOURCE-01: rendered signature missing oracle types %q / %q\n--- src ---\n%s",
			wantReq, wantResp, src)
	}
}

// TestGRPC_PROTO_REGISTRY_SINGLE_SOURCE_01_PopulatesProtoFields asserts the
// builder fills spec.GRPC's proto fields from the proto oracle (the single write
// site C1 locks).
func TestGRPC_PROTO_REGISTRY_SINGLE_SOURCE_01_PopulatesProtoFields(t *testing.T) {
	t.Parallel()
	oracle, err := readProtoTypeInfo(fixtureProtoPath(t), "IssueCommand")
	if err != nil {
		t.Fatalf("oracle: %v", err)
	}
	testDir, err := filepath.Abs(filepath.Join("testdata", "synth", "synth_grpc_minimal"))
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	p, err := metadata.NewParser(testDir).Parse()
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	spec, err := buildContractSpec(testDir, p, grpcSingleSourceContractID)
	if err != nil {
		t.Fatalf("buildContractSpec: %v", err)
	}
	g := spec.GRPC
	if g == nil {
		t.Fatal("spec.GRPC is nil")
	}
	if g.ProtoImportPath != oracle.ImportPath || g.ProtoAlias != oracle.Alias ||
		g.RequestType != oracle.RequestType || g.ResponseType != oracle.ResponseType {
		t.Errorf("spec.GRPC proto fields != oracle:\n got: %+v\nwant import=%q alias=%q req=%q resp=%q",
			g, oracle.ImportPath, oracle.Alias, oracle.RequestType, oracle.ResponseType)
	}
}

// _ImportOracleDetectorIsLive proves the C3 import extraction+compare fires when
// the rendered import diverges from the oracle, so a green result means "import
// matches proto" rather than "detector never fires".
func TestGRPC_PROTO_REGISTRY_SINGLE_SOURCE_01_ImportOracleDetectorIsLive(t *testing.T) {
	t.Parallel()
	const oracle = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1"

	divergent := []byte(`package command

import (
	"context"

	commandv1 "github.com/ghbvf/gocell/generated/contracts/grpc/EVIL/v1"
)

type Server interface {
	IssueCommand(ctx context.Context, req *commandv1.IssueCommandRequest) (*commandv1.IssueCommandResponse, error)
}
`)
	got := nonContextImports(t, divergent)
	if len(got) != 1 || got[0] == oracle {
		t.Fatalf("detector should extract the divergent import != oracle, got %v", got)
	}

	clean := []byte(`package command

import (
	"context"

	commandv1 "` + oracle + `"
)
`)
	if imps := nonContextImports(t, clean); len(imps) != 1 || imps[0] != oracle {
		t.Fatalf("detector false result on clean stub: %v", imps)
	}
}

// --- C1: GRPCEndpointSpec is constructed only in buildGRPCSpec ----------------

func TestGRPC_PROTO_REGISTRY_SINGLE_SOURCE_01_SpecConstructedOnlyInBuilder(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	files := parsePackageProdFiles(t, fset, ".")
	bad := grpcSpecConstructorsOutside(fset, files, "buildGRPCSpec")
	if len(bad) > 0 {
		t.Errorf("GRPC-PROTO-REGISTRY-SINGLE-SOURCE-01: GRPCEndpointSpec composite literal outside buildGRPCSpec at:\n%s",
			strings.Join(bad, "\n"))
	}
}

// _SpecConstructorDetectorIsLive proves the C1 walker flags a GRPCEndpointSpec
// literal built in a non-builder function.
func TestGRPC_PROTO_REGISTRY_SINGLE_SOURCE_01_SpecConstructorDetectorIsLive(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	src := `package contractgen

func evilConstructor() *GRPCEndpointSpec {
	return &GRPCEndpointSpec{InterfaceName: "Server", ProtoImportPath: "hardcoded"}
}
`
	f, err := parser.ParseFile(fset, "evil.go", src, 0)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	bad := grpcSpecConstructorsOutside(fset, []*ast.File{f}, "buildGRPCSpec")
	if len(bad) != 1 {
		t.Fatalf("detector should flag the non-builder constructor, got %v", bad)
	}
}

// grpcSpecConstructorsOutside returns positions of GRPCEndpointSpec composite
// literals NOT lexically inside a FuncDecl named allowedFunc.
func grpcSpecConstructorsOutside(fset *token.FileSet, files []*ast.File, allowedFunc string) []string {
	var bad []string
	for _, f := range files {
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			allowed := fn.Name.Name == allowedFunc
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				cl, ok := n.(*ast.CompositeLit)
				if !ok {
					return true
				}
				if id, ok := cl.Type.(*ast.Ident); ok && id.Name == "GRPCEndpointSpec" && !allowed {
					bad = append(bad, fset.Position(cl.Pos()).String())
				}
				return true
			})
		}
	}
	return bad
}

// --- C4: registry collision uniqueness ----------------------------------------

func sampleProtoInfo() protoTypeInfo {
	return protoTypeInfo{
		ProtoPackage: "device.command.v1",
		ImportPath:   "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1",
		Alias:        "commandv1",
		RequestType:  "IssueCommandRequest",
		ResponseType: "IssueCommandResponse",
	}
}

func TestGRPC_PROTO_REGISTRY_SINGLE_SOURCE_01_CollisionDistinctMethodsOK(t *testing.T) {
	t.Parallel()
	r := newProtoRegistry()
	if err := r.register("grpc.a", "device.command.v1.DeviceCommandService", "IssueCommand", sampleProtoInfo()); err != nil {
		t.Fatalf("first register: %v", err)
	}
	if err := r.register("grpc.b", "device.command.v1.DeviceCommandService", "RevokeCommand", sampleProtoInfo()); err != nil {
		t.Fatalf("distinct method on same service must be allowed: %v", err)
	}
}

func TestGRPC_PROTO_REGISTRY_SINGLE_SOURCE_01_CollisionSameMethod(t *testing.T) {
	t.Parallel()
	r := newProtoRegistry()
	if err := r.register("grpc.a", "device.command.v1.DeviceCommandService", "IssueCommand", sampleProtoInfo()); err != nil {
		t.Fatalf("first register: %v", err)
	}
	err := r.register("grpc.b", "device.command.v1.DeviceCommandService", "IssueCommand", sampleProtoInfo())
	if err == nil || !strings.Contains(err.Error(), "already registered") {
		t.Fatalf("expected duplicate (package,service,method) collision, got %v", err)
	}
}

func TestGRPC_PROTO_REGISTRY_SINGLE_SOURCE_01_CollisionDivergentImport(t *testing.T) {
	t.Parallel()
	r := newProtoRegistry()
	if err := r.register("grpc.a", "device.command.v1.DeviceCommandService", "IssueCommand", sampleProtoInfo()); err != nil {
		t.Fatalf("first register: %v", err)
	}
	other := sampleProtoInfo()
	other.ImportPath = "github.com/ghbvf/gocell/generated/contracts/grpc/device/command/v1/evil"
	err := r.register("grpc.b", "device.command.v1.DeviceCommandService", "RevokeCommand", other)
	if err == nil || !strings.Contains(err.Error(), "divergent import") {
		t.Fatalf("expected divergent-import collision, got %v", err)
	}
}

// --- shared helpers -----------------------------------------------------------

// nonContextImports parses Go source (imports only) and returns every import
// path that is not "context".
func nonContextImports(t *testing.T, src []byte) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "rendered.go", src, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse rendered: %v\n--- src ---\n%s", err, src)
	}
	var out []string
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatalf("unquote import %q: %v", imp.Path.Value, err)
		}
		if path != "context" {
			out = append(out, path)
		}
	}
	return out
}

// parsePackageProdFiles parses every non-test .go file in dir.
func parsePackageProdFiles(t *testing.T, fset *token.FileSet, dir string) []*ast.File {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir %q: %v", dir, err)
	}
	var files []*ast.File
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %q: %v", name, err)
		}
		files = append(files, f)
	}
	return files
}
