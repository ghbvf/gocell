// INVARIANT: SEALED-MARKER-NOOP-TRANSPARENCY-01
//
// SEALED-MARKER-NOOP-TRANSPARENCY-01 — every internalCell* concrete type in
// kernel/{persistence,outbox}/cell_marker.go must declare a `Noop() bool`
// method. Without this method the sealed wrapper hides the inner Nooper
// signal from cell.CheckNotNoop / mode_resolver.isNooperDep, letting durable
// assemblies silently accept demo runners/publishers/writers.
//
// AI-rebust 评级：Medium (AST receiver-type scan — type-aware on method
// name + receiver identifier). Upgraded to Hard path is blocked: the
// internalCell* types are unexported so go/types canonical cannot be used
// without loading the package; AST scan is the appropriate tool here.
//
// **行为覆盖分工**：本 archtest 仅守 "method 存在 + 签名"（结构性约束）；
// runtime 透传行为（inner=nooper → wrapped.Noop()=true / inner=non-nooper
// → wrapped.Noop()=false）由 unit test 守 —
//   - kernel/persistence/cell_marker_test.go::TestWrapForCell_PreservesNooperPassThrough
//   - kernel/persistence/cell_marker_test.go::TestWrapForCell_NonNooperReturnsFalse
//   - kernel/outbox/cell_marker_test.go::TestWrapPublisherForCell_PreservesNooperPassThrough
//   - kernel/outbox/cell_marker_test.go::TestWrapWriterForCell_PreservesNooperPassThrough
//
// archtest 加 runtime 行为断言是反模式（static analysis 工具不应内嵌运行时）；
// 双层防线分工已完整。
//
// ref: docs/architecture/202605101900-adr-cell-raw-infra-sealed-marker.md §D1 Noop passthrough
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// sealedMarkerFiles 列出所有需扫的 sealed marker 实施文件。
// **维护约定**：新增 sealed marker 包（如 PR-A23 引入的 kernel/outbox CellEmitter）
// 必须同步追加到此列表，否则 archtest 静默跳过新文件。
// Hard 升级路径见 backlog SEALED-MARKER-FILE-LIST-AUTODISCOVER-01。
var sealedMarkerFiles = []struct {
	rel    string // path relative to module root
	prefix string // unexported type name prefix to scan
}{
	{"kernel/persistence/cell_marker.go", "internalCell"},
	{"kernel/outbox/cell_marker.go", "internalCell"},
}

// INVARIANT: SEALED-MARKER-NOOP-TRANSPARENCY-01
//
// TestSealedMarkerNoopTransparency01 asserts that every `internalCell*`
// concrete type declared in the sealed marker files has a `Noop() bool`
// method. This prevents a refactor from silently removing the Noop
// pass-through and breaking cell.CheckNotNoop's durable-mode rejection.
func TestSealedMarkerNoopTransparency01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	for _, entry := range sealedMarkerFiles {
		entry := entry
		t.Run(entry.rel, func(t *testing.T) {
			t.Parallel()
			absPath := filepath.Join(root, filepath.FromSlash(entry.rel))
			fset := token.NewFileSet()
			f, err := parser.ParseFile(fset, absPath, nil, 0)
			if err != nil {
				t.Fatalf("SEALED-MARKER-NOOP-TRANSPARENCY-01: parse %s: %v", entry.rel, err)
			}

			// Collect all internalCell* struct type names via scanner.EachInSubtree[ast.TypeSpec].
			// scanner.EachInSubtree uses ast.Preorder; TypeSpec nodes only appear under GenDecl
			// at file scope, so preorder yields the same set as a manual Decls/Specs walk.
			internalTypes := map[string]bool{}
			scanner.EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
				if strings.HasPrefix(ts.Name.Name, entry.prefix) {
					if _, isStruct := ts.Type.(*ast.StructType); isStruct {
						internalTypes[ts.Name.Name] = false // false = Noop not yet found
					}
				}
			})

			if len(internalTypes) == 0 {
				t.Fatalf("SEALED-MARKER-NOOP-TRANSPARENCY-01: no %s* struct types found in %s",
					entry.prefix, entry.rel)
			}

			// Walk function declarations and find `Noop() bool` methods on
			// each internalCell* receiver. receiverTypeName is defined in
			// pg_repo_ambient_tx_test.go (package-level helper shared across
			// tests in this package).
			//
			// scanner.EachInSubtree[ast.FuncDecl] is used per SCANNER-FRAMEWORK-USAGE-01.
			// FuncDecl only appears at file-scope in Go AST (function literals are
			// ast.FuncLit, not ast.FuncDecl), so preorder yields the same set as
			// a manual Decls walk.
			scanner.EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
				if fd.Recv == nil || fd.Name.Name != "Noop" {
					return
				}
				// Check return type is `bool`.
				if !noopFuncReturnsOnlyBool(fd) {
					return
				}
				// Identify receiver type name via shared receiverTypeName helper.
				recv := receiverTypeName(fd)
				if recv != "" {
					if _, monitored := internalTypes[recv]; monitored {
						internalTypes[recv] = true
					}
				}
			})

			for typeName, hasNoop := range internalTypes {
				if !hasNoop {
					t.Errorf("SEALED-MARKER-NOOP-TRANSPARENCY-01: %s in %s missing Noop() bool method — "+
						"sealed wrapper must expose inner Nooper signal for cell.CheckNotNoop / isNooperDep",
						typeName, entry.rel)
				}
			}
		})
	}
}

// noopFuncReturnsOnlyBool reports whether a function declaration has exactly
// one result that is the identifier "bool". Local to sealed_marker tests;
// named to avoid collision with other helpers in the archtest package.
func noopFuncReturnsOnlyBool(fd *ast.FuncDecl) bool {
	if fd.Type.Results == nil || fd.Type.Results.NumFields() != 1 {
		return false
	}
	ident, ok := fd.Type.Results.List[0].Type.(*ast.Ident)
	return ok && ident.Name == "bool"
}
