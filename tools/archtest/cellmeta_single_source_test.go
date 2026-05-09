// CELLMETA-SINGLE-SOURCE-01..03 — kernel/cell ↔ kernel/metadata single-source gates.
//
// kernel/cell.CellMetadata + 4 衍生类型（Owner / SchemaConfig / CellVerify / L0Dep）
// 与 kernel/metadata.CellMeta + 衍生 type 长期并存且字段已漂移 5 项
// （DurabilityMode / Listeners / GoStructName / Dir / File 仅在 metadata 侧）。
// PR-A1 物理合一后所有 cell 元数据类型集中在 kernel/metadata。
//
// Gate IDs:
//
//	CELLMETA-SINGLE-SOURCE-01    kernel/cell 不得定义 CellMetadata / Owner /
//	                              SchemaConfig / CellVerify / L0Dep 5 类型
//	CELLMETA-SINGLE-SOURCE-02    kernel/cell.NewBaseCell 接收单一 *metadata.CellMeta
//	CELLMETA-SINGLE-SOURCE-03    Cell interface 的 Metadata() 返回 *metadata.CellMeta
//
// Known limits (documented for future maintainers, not blocking current contract):
//
//   - Gate-01 forbidden list is a closed set of historical type names; a
//     "rename and re-introduce" pattern (e.g. type LegacyCellMeta = metadata.CellMeta)
//     in kernel/cell would not be caught. Reviewers must catch such aliases.
//   - Gate-03 walks Cell interface methods by direct declaration only; if
//     Metadata() is moved to an embedded sub-interface (type Cell interface { MetadataReader; ... }),
//     this gate would falsely report "method not found". Refactor with care.
//
// ref: docs/plans/202605011500-029-master-roadmap.md K#05 PR-A1
// ref: docs/architecture/202605051300-adr-kernel-cellmeta-single-source.md
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCellmetaSingleSource01_NoForbiddenTypes verifies CELLMETA-SINGLE-SOURCE-01.
// kernel/cell/*.go (excluding _test.go) 必须不得声明 5 个被合并的类型。
//
// SCANNER-ESCAPE-HATCH: deferred-scanner-migration
// Scans kernel/cell/*.go via os.ReadDir; predates scanner framework, candidate
// for scanner.EachFile migration when next touched.
func TestCellmetaSingleSource01_NoForbiddenTypes(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	cellDir := filepath.Join(root, "kernel", "cell")
	forbidden := map[string]bool{
		"CellMetadata": true,
		"Owner":        true,
		"SchemaConfig": true,
		"CellVerify":   true,
		"L0Dep":        true,
	}
	entries, err := os.ReadDir(cellDir)
	if err != nil {
		t.Fatalf("read kernel/cell: %v", err)
	}
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(cellDir, name)
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", path, perr)
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.TYPE {
				continue
			}
			for _, spec := range gd.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				if forbidden[ts.Name.Name] {
					t.Errorf(
						"CELLMETA-SINGLE-SOURCE-01: %s declares type %s — "+
							"moved to kernel/metadata (CellMeta / OwnerMeta / SchemaMeta / "+
							"CellVerifyMeta / L0DepMeta); remove duplicate type",
						path, ts.Name.Name,
					)
				}
			}
		}
	}
}

// TestCellmetaSingleSource02_NewBaseCellSignature verifies CELLMETA-SINGLE-SOURCE-02.
// kernel/cell.NewBaseCell 必须接收 1 个参数且类型为 *metadata.CellMeta。
func TestCellmetaSingleSource02_NewBaseCellSignature(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	path := filepath.Join(root, "kernel", "cell", "base.go")
	fset := token.NewFileSet()
	f, perr := parser.ParseFile(fset, path, nil, 0)
	if perr != nil {
		t.Fatalf("parse %s: %v", path, perr)
	}
	var found *ast.FuncDecl
	for _, decl := range f.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || fd.Name == nil {
			continue
		}
		if fd.Name.Name == "NewBaseCell" {
			found = fd
			break
		}
	}
	if found == nil {
		t.Fatal("CELLMETA-SINGLE-SOURCE-02: NewBaseCell not found in kernel/cell/base.go")
	}
	if found.Type.Params == nil || cellmetaParamCount(found.Type.Params) != 1 {
		t.Fatalf("CELLMETA-SINGLE-SOURCE-02: NewBaseCell must take exactly 1 parameter, got %d",
			cellmetaParamCount(found.Type.Params))
	}
	got := exprString(found.Type.Params.List[0].Type)
	if got != "*metadata.CellMeta" {
		t.Errorf("CELLMETA-SINGLE-SOURCE-02: NewBaseCell parameter type = %q, want %q",
			got, "*metadata.CellMeta")
	}
}

// TestCellmetaSingleSource03_MetadataInterfaceReturn verifies CELLMETA-SINGLE-SOURCE-03.
// Cell interface.Metadata() 必须返回 *metadata.CellMeta（pointer，零拷贝）。
func TestCellmetaSingleSource03_MetadataInterfaceReturn(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	path := filepath.Join(root, "kernel", "cell", "interfaces.go")
	fset := token.NewFileSet()
	f, perr := parser.ParseFile(fset, path, nil, 0)
	if perr != nil {
		t.Fatalf("parse %s: %v", path, perr)
	}
	var cellIface *ast.InterfaceType
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.TYPE {
			continue
		}
		for _, spec := range gd.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok || ts.Name.Name != "Cell" {
				continue
			}
			iface, ok := ts.Type.(*ast.InterfaceType)
			if !ok {
				continue
			}
			cellIface = iface
			break
		}
	}
	if cellIface == nil {
		t.Fatal("CELLMETA-SINGLE-SOURCE-03: Cell interface not found in kernel/cell/interfaces.go")
	}
	var metaMethod *ast.Field
	for _, m := range cellIface.Methods.List {
		if len(m.Names) > 0 && m.Names[0].Name == "Metadata" {
			metaMethod = m
			break
		}
	}
	if metaMethod == nil {
		t.Fatal("CELLMETA-SINGLE-SOURCE-03: Cell.Metadata() method not found")
	}
	fn, ok := metaMethod.Type.(*ast.FuncType)
	if !ok {
		t.Fatal("CELLMETA-SINGLE-SOURCE-03: Cell.Metadata is not a function type")
	}
	if fn.Results == nil || len(fn.Results.List) != 1 {
		t.Fatal("CELLMETA-SINGLE-SOURCE-03: Cell.Metadata must return exactly one value")
	}
	got := exprString(fn.Results.List[0].Type)
	if got != "*metadata.CellMeta" {
		t.Errorf("CELLMETA-SINGLE-SOURCE-03: Cell.Metadata return type = %q, want %q",
			got, "*metadata.CellMeta")
	}
}

// cellmetaParamCount counts named + anonymous parameters in a FieldList.
// Reuses the same convention as the archtest package's existing AST helpers.
func cellmetaParamCount(fl *ast.FieldList) int {
	if fl == nil {
		return 0
	}
	n := 0
	for _, f := range fl.List {
		if len(f.Names) == 0 {
			n++
		} else {
			n += len(f.Names)
		}
	}
	return n
}
