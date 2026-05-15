// invariants:
//   - INVARIANT: CELLMETA-SINGLE-SOURCE-01
//   - INVARIANT: CELLMETA-SINGLE-SOURCE-02
//   - INVARIANT: CELLMETA-SINGLE-SOURCE-03
//
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
//	                              SchemaConfig / CellVerify / L0Dep 5 类型（旧 struct
//	                              名；PR-A22 ISP 拆分使用 CellInventory 不冲突）
//	CELLMETA-SINGLE-SOURCE-02    kernel/cell.NewBaseCell 接收单一 *metadata.CellMeta
//	CELLMETA-SINGLE-SOURCE-03    CellInventory sub-interface 的 Metadata() 返回 *metadata.CellMeta
//
// Known limits (documented for future maintainers, not blocking current contract):
//
//   - Gate-01 forbidden list is a closed set of historical type names; a
//     "rename and re-introduce" pattern (e.g. type LegacyCellMeta = metadata.CellMeta)
//     in kernel/cell would not be caught. Reviewers must catch such aliases.
//   - Gate-03 / CellInventory.Metadata():
//     PR-A22 (ADR 202605101800 §D5) moved Metadata() from top-level Cell to the
//     CellInventory sub-interface; the previous limit ("Metadata() on embedded
//     sub-interface false-fails") is now resolved — this gate explicitly scans
//     the CellInventory type. Keep this note as a regression marker.
//
// ref: docs/plans/202605011500-029-master-roadmap.md K#05 PR-A1
// ref: docs/architecture/202605051300-adr-kernel-cellmeta-single-source.md
// ref: docs/architecture/202605101800-adr-cell-interface-isp-split.md D5（PR-A22 SOURCE-03 升级目标接口至 CellInventory）
package archtest

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// TestCellmetaSingleSource01_NoForbiddenTypes verifies CELLMETA-SINGLE-SOURCE-01.
// kernel/cell/*.go (excluding _test.go) 必须不得声明 5 个被合并的类型。
func TestCellmetaSingleSource01_NoForbiddenTypes(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	forbidden := map[string]bool{
		"CellMetadata": true,
		"Owner":        true,
		"SchemaConfig": true,
		"CellVerify":   true,
		"L0Dep":        true,
	}
	scope := DirsScope(root, []string{"kernel/cell"})
	Run(t, scope, func(p *Pass) []Diagnostic {
		for _, file := range p.Files {
			EachInSubtree[ast.TypeSpec](file, func(ts *ast.TypeSpec) {
				if forbidden[ts.Name.Name] {
					t.Errorf(
						"CELLMETA-SINGLE-SOURCE-01: %s declares type %s — "+
							"moved to kernel/metadata (CellMeta / OwnerMeta / SchemaMeta / "+
							"CellVerifyMeta / L0DepMeta); remove duplicate type",
						p.Rel(file), ts.Name.Name,
					)
				}
			})
		}
		return nil
	})
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
	EachInSubtree[ast.FuncDecl](f, func(fd *ast.FuncDecl) {
		if found != nil || fd.Recv != nil || fd.Name == nil {
			return
		}
		if fd.Name.Name == "NewBaseCell" {
			found = fd
		}
	})
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
// CellInventory sub-interface.Metadata() 必须返回 *metadata.CellMeta（pointer，零拷贝）。
// PR-A22 ISP 拆分把 Metadata() 从顶层 Cell 接口下放到 CellInventory 子接口，本 gate
// 同步升级以扫子接口而非顶层（D5）。
func TestCellmetaSingleSource03_MetadataInterfaceReturn(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	path := filepath.Join(root, "kernel", "cell", "interfaces.go")
	fset := token.NewFileSet()
	f, perr := parser.ParseFile(fset, path, nil, 0)
	if perr != nil {
		t.Fatalf("parse %s: %v", path, perr)
	}
	var inventoryIface *ast.InterfaceType
	EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
		if inventoryIface != nil || ts.Name.Name != "CellInventory" {
			return
		}
		iface, ok := ts.Type.(*ast.InterfaceType)
		if !ok {
			return
		}
		inventoryIface = iface
	})
	if inventoryIface == nil {
		t.Fatal("CELLMETA-SINGLE-SOURCE-03: CellInventory sub-interface not found in kernel/cell/interfaces.go " +
			"(PR-A22 places Metadata() on CellInventory, not top-level Cell)")
	}
	var metaMethod *ast.Field
	for _, m := range inventoryIface.Methods.List {
		if len(m.Names) > 0 && m.Names[0].Name == "Metadata" {
			metaMethod = m
			break
		}
	}
	if metaMethod == nil {
		t.Fatal("CELLMETA-SINGLE-SOURCE-03: CellInventory.Metadata() method not found")
	}
	fn, ok := metaMethod.Type.(*ast.FuncType)
	if !ok {
		t.Fatal("CELLMETA-SINGLE-SOURCE-03: CellInventory.Metadata is not a function type")
	}
	if fn.Results == nil || len(fn.Results.List) != 1 {
		t.Fatal("CELLMETA-SINGLE-SOURCE-03: CellInventory.Metadata must return exactly one value")
	}
	got := exprString(fn.Results.List[0].Type)
	if got != "*metadata.CellMeta" {
		t.Errorf("CELLMETA-SINGLE-SOURCE-03: CellInventory.Metadata return type = %q, want %q",
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
