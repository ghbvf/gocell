// INVARIANT: GRPC-CODEGEN-NO-PROTO-DEP-01
//
// The grpc contract generator emits a PLACEHOLDER server interface whose
// request/response are []byte. Until PR 6 wires the proto toolchain, the
// rendered stub MUST NOT import google.golang.org/protobuf or
// google.golang.org/grpc — pulling either in here would (a) leak the proto
// runtime into generated code before the go.mod dependency is introduced and
// (b) defeat the "buildable without proto" property PR 2 exists to validate.
//
// This rule is TRANSIENT: PR 6 ("codegen real proto integration") replaces the
// []byte placeholder with proto-generated message types, at which point both
// imports become legitimate and this file is deleted (see
// docs/plans/specs/202605262300-048-grpc-adapter/plan.md PR 6).
//
// AI-robust grading: Hard. The rule does not glob committed goldens by filename
// (a Soft name-convention — a rename would silently drop coverage). Instead it
// RENDERS the stub fresh through the generator's own render path and AST-walks
// the import set, so it is non-vacuous every run and survives `go test -update`
// (an operator who adds a forbidden import to iface.tmpl and regenerates the
// golden is still caught). Co-located in package contractgen because the truth
// source is the render funnel (buildContractSpec + renderFile), reachable only
// from inside the package; the literal generated/contracts/grpc/** tree is empty
// for this rule's entire PR-2→PR-6 lifetime (first real grpc contract is PR 8),
// so a module-scope import scan would be permanently vacuous.
//
// Blind spots of the chosen tool (go/parser import walk) + reverse self-check:
//   - import alias (`pb "google.golang.org/protobuf/proto"`) and dot-import
//     (`. "..."`) both still produce an *ast.ImportSpec whose Path is the module
//     string, so prefix matching is alias-agnostic. TestGRPC_CODEGEN_NO_PROTO_DEP_01_DetectorIsLive
//     feeds a snippet containing exactly such an import and asserts it IS flagged,
//     proving the detector is not a vacuous always-pass.

package contractgen

import (
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// grpcForbiddenImportModules are the module roots the placeholder grpc stub must
// not depend on before PR 6. Subpackages (mod + "/...") are forbidden too.
var grpcForbiddenImportModules = []string{
	"google.golang.org/protobuf",
	"google.golang.org/grpc",
}

// forbiddenProtoImports parses Go source and returns the import paths that are,
// or are subpackages of, any grpcForbiddenImportModules entry. label is used in
// failure diagnostics. A parse failure is fatal (a stub that does not parse is a
// generator bug, not a pass).
func forbiddenProtoImports(t *testing.T, label string, src []byte) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, label, src, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parse rendered %s: %v\n--- source ---\n%s", label, err, src)
	}
	var found []string
	for _, imp := range f.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatalf("unquote import %q in %s: %v", imp.Path.Value, label, err)
		}
		for _, mod := range grpcForbiddenImportModules {
			if path == mod || strings.HasPrefix(path, mod+"/") {
				found = append(found, path)
			}
		}
	}
	return found
}

// TestGRPC_CODEGEN_NO_PROTO_DEP_01 renders the grpc placeholder stub fresh and
// asserts no forbidden proto/grpc import reaches any emitted artifact.
func TestGRPC_CODEGEN_NO_PROTO_DEP_01(t *testing.T) {
	t.Parallel()
	testDir, err := filepath.Abs(filepath.Join("testdata", "synth", "synth_grpc_minimal"))
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	p, err := metadata.NewParser(testDir).Parse()
	if err != nil {
		t.Fatalf("parse synth_grpc_minimal: %v", err)
	}
	const contractID = "grpc.device.command.v1"
	if p.Contracts[contractID] == nil {
		t.Fatalf("%s not found in synth_grpc_minimal fixture", contractID)
	}

	// Every artifact a grpc contract emits (types_gen.go + iface_gen.go).
	for _, outFile := range []string{"types_gen.go", "iface_gen.go"} {
		t.Run(outFile, func(t *testing.T) {
			t.Parallel()
			spec, err := buildContractSpec(testDir, p, contractID)
			if err != nil {
				t.Fatalf("buildContractSpec: %v", err)
			}
			src := renderFile(t, spec, outFile)
			if bad := forbiddenProtoImports(t, outFile, src); len(bad) > 0 {
				t.Errorf("GRPC-CODEGEN-NO-PROTO-DEP-01: rendered %s imports forbidden module(s) %v; "+
					"the PR-2 placeholder must use []byte and stay free of proto/grpc deps until PR 6",
					outFile, bad)
			}
		})
	}
}

// TestGRPC_CODEGEN_NO_PROTO_DEP_01_DetectorIsLive is the reverse self-check: it
// proves forbiddenProtoImports actually flags forbidden imports across the
// aliased, plain, and dot-import forms, so TestGRPC_CODEGEN_NO_PROTO_DEP_01's
// green result means "no forbidden import" rather than "detector never fires".
func TestGRPC_CODEGEN_NO_PROTO_DEP_01_DetectorIsLive(t *testing.T) {
	t.Parallel()
	// Aliased, plain, and dot imports all carry the module string in
	// ImportSpec.Path (forbiddenProtoImports reads Path, never the alias/Name),
	// so all three forms are flagged. (parser.ImportsOnly stops after the import
	// block, so no symbol references are needed.)
	redSource := []byte(`package command

import (
	pb "google.golang.org/protobuf/proto"
	"google.golang.org/grpc"
	. "google.golang.org/grpc/codes"
)
`)
	bad := forbiddenProtoImports(t, "red_fixture.go", redSource)
	if len(bad) != 3 {
		t.Fatalf("detector should flag all 3 forbidden imports (aliased + plain + dot), got %v", bad)
	}

	// A clean stub (the actual placeholder shape) must NOT be flagged.
	greenSource := []byte(`package command

import "context"

type Server interface {
	IssueCommand(ctx context.Context, req []byte) ([]byte, error)
}
`)
	if bad := forbiddenProtoImports(t, "green_fixture.go", greenSource); len(bad) > 0 {
		t.Fatalf("detector false-positive on clean stub: %v", bad)
	}
}
