// invariants:
//   - INVARIANT: CELL-IFACE-ISP-COMPOSITE-01
//   - INVARIANT: CELL-IFACE-ISP-METHODSETS-01
//   - INVARIANT: CELL-IFACE-ISP-BASECELL-CHECK-01
//
// CELL-IFACE-ISP-* — kernel/cell.Cell ISP 拆分守卫。
//
// PR-A22 把 12 方法的 Cell 接口按 ISP 切分为 4 子接口（CellIdentity / CellLifecycle /
// CellStatus / CellInventory）+ 复合 Cell。这 3 条 invariant 锁定形态不退化：
//
//	CELL-IFACE-ISP-COMPOSITE-01     Cell interface 必须是 4 子接口的纯内嵌复合
//	CELL-IFACE-ISP-METHODSETS-01    每个子接口的方法集合必须精确匹配契约
//	CELL-IFACE-ISP-BASECELL-CHECK-01 BaseCell compile-time check 必须四段式分写
//
// AI-rebust 评级：Medium（AST type-aware 识别 interface embedded type expression）。
//
// ref: docs/architecture/202605101800-adr-cell-interface-isp-split.md D1/D2/D3
// ref: kubernetes/apimachinery/pkg/apis/meta/v1.ObjectMetaAccessor + io.ReadWriter pattern
package archtest

import (
	"crypto/sha256"
	"encoding/hex"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// expectedSubInterfaces names the 4 sub-interfaces the Cell composite must
// embed (and only these). Order is irrelevant; the assertion is set equality.
var expectedSubInterfaces = []string{
	"CellIdentity",
	"CellLifecycle",
	"CellStatus",
	"CellInventory",
}

// expectedSubInterfaceMethods captures the canonical method set for each
// sub-interface. Adding/removing methods here is a contract change that
// must be paired with an ADR amendment.
var expectedSubInterfaceMethods = map[string][]string{
	"CellIdentity":  {"ID", "Type", "ConsistencyLevel"},
	"CellLifecycle": {"Init", "Start", "Stop"},
	"CellStatus":    {"Health", "Ready"},
	"CellInventory": {"Metadata", "OwnedSlices", "ProducedContracts", "ConsumedContracts"},
}

// TestCellIfaceISP01_CellComposesFourSubInterfaces verifies CELL-IFACE-ISP-COMPOSITE-01.
// kernel/cell.Cell 必须是 4 子接口的纯内嵌复合，本身不直接声明任何方法。
func TestCellIfaceISP01_CellComposesFourSubInterfaces(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	cellIface := loadInterfaceType(t, root, "Cell")
	if cellIface == nil {
		t.Fatal("CELL-IFACE-ISP-COMPOSITE-01: Cell interface not found in kernel/cell/interfaces.go")
	}

	got := embeddedTypeNames(cellIface)
	if len(got) == 0 {
		t.Fatalf("CELL-IFACE-ISP-COMPOSITE-01: Cell interface embeds no sub-interfaces; want %v",
			expectedSubInterfaces)
	}
	if directMethods := directMethodNames(cellIface); len(directMethods) > 0 {
		t.Errorf("CELL-IFACE-ISP-COMPOSITE-01: Cell interface declares direct methods %v; "+
			"all methods must live on sub-interfaces (CellIdentity/CellLifecycle/CellStatus/CellInventory)",
			directMethods)
	}

	want := append([]string(nil), expectedSubInterfaces...)
	sort.Strings(want)
	gotSorted := append([]string(nil), got...)
	sort.Strings(gotSorted)
	if !equalStringSlices(gotSorted, want) {
		t.Errorf("CELL-IFACE-ISP-COMPOSITE-01: Cell embedded sub-interfaces = %v, want exactly %v",
			gotSorted, want)
	}
}

// TestCellIfaceISP02_SubInterfaceMethodSets verifies CELL-IFACE-ISP-METHODSETS-01.
// 每个子接口必须声明精确的方法集合（不多不少）。
func TestCellIfaceISP02_SubInterfaceMethodSets(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	for _, name := range expectedSubInterfaces {
		name := name
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			iface := loadInterfaceType(t, root, name)
			if iface == nil {
				t.Fatalf("CELL-IFACE-ISP-METHODSETS-01: sub-interface %s not found in kernel/cell/interfaces.go", name)
			}
			gotMethods := directMethodNames(iface)
			sort.Strings(gotMethods)

			want := append([]string(nil), expectedSubInterfaceMethods[name]...)
			sort.Strings(want)

			if !equalStringSlices(gotMethods, want) {
				t.Errorf("CELL-IFACE-ISP-METHODSETS-01: %s methods = %v, want exactly %v",
					name, gotMethods, want)
			}
		})
	}
}

// TestCellIfaceISP03_BaseCellFourSegmentCheck verifies CELL-IFACE-ISP-BASECELL-CHECK-01.
// kernel/cell/base.go 必须含 4 行独立 compile-time check（每子接口一行），
// 不得退化为单条 `_ Cell = (*BaseCell)(nil)`。
func TestCellIfaceISP03_BaseCellFourSegmentCheck(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	path := filepath.Join(root, "kernel", "cell", "base.go")
	fset := token.NewFileSet()
	f, perr := parser.ParseFile(fset, path, nil, 0)
	if perr != nil {
		t.Fatalf("parse %s: %v", path, perr)
	}

	// Collect all `var _ Iface = (*BaseCell)(nil)` checks across file's
	// ValueSpec nodes. Walks via scanner.EachInSubtree[ast.ValueSpec] (the funnel
	// SCANNER-FRAMEWORK-USAGE-01 mandates) instead of for-range over
	// f.Decls + nested type assertions.
	subSeen := make(map[string]bool)
	plainCellSeen := false
	scanner.EachInSubtree[ast.ValueSpec](f, func(vs *ast.ValueSpec) {
		if !isBlankIdentList(vs.Names) {
			return
		}
		ifaceName := exprString(vs.Type)
		if ifaceName == "" {
			return
		}
		if !targetsBaseCellNilPtr(vs.Values) {
			return
		}
		if ifaceName == "Cell" {
			plainCellSeen = true
			return
		}
		subSeen[ifaceName] = true
	})

	if plainCellSeen {
		t.Errorf("CELL-IFACE-ISP-BASECELL-CHECK-01: kernel/cell/base.go must not retain " +
			"`var _ Cell = (*BaseCell)(nil)` after ISP split; replace with the four sub-interface checks")
	}
	t.Logf("found checks: %v", subSeen)
	for _, name := range expectedSubInterfaces {
		if !subSeen[name] {
			t.Errorf("CELL-IFACE-ISP-BASECELL-CHECK-01: kernel/cell/base.go missing "+
				"`var _ %s = (*BaseCell)(nil)` compile-time check", name)
		}
	}
}

// ---------- helpers (file-local) ----------

// loadInterfaceType scans all non-test *.go files in kernel/cell/ and returns
// the named *ast.InterfaceType, or nil if the named type does not exist or is
// not an interface. Scanning the whole package (not just interfaces.go) ensures
// the invariant is not defeated by moving a type declaration to a new file.
// Uses scanner.DirsScope to enumerate files (SCANNER-FRAMEWORK-USAGE-01 compliant).
func loadInterfaceType(t *testing.T, root, name string) *ast.InterfaceType {
	t.Helper()
	scope := scanner.DirsScope(root, []string{"kernel/cell"})
	files, err := scope.Files()
	if err != nil {
		t.Fatalf("scanner.DirsScope kernel/cell: %v", err)
	}
	if len(files) == 0 {
		t.Fatal("loadInterfaceType: no .go files found in kernel/cell")
	}
	for _, path := range files {
		fset := token.NewFileSet()
		f, perr := parser.ParseFile(fset, path, nil, 0)
		if perr != nil {
			t.Fatalf("parse %s: %v", filepath.Base(path), perr)
		}
		var found *ast.InterfaceType
		var bail bool
		scanner.EachInSubtree[ast.TypeSpec](f, func(ts *ast.TypeSpec) {
			if found != nil || bail || ts.Name.Name != name {
				return
			}
			iface, ok := ts.Type.(*ast.InterfaceType)
			if !ok {
				bail = true
				return
			}
			found = iface
		})
		if found != nil {
			return found
		}
		if bail {
			return nil
		}
	}
	return nil
}

// embeddedTypeNames returns the names of types embedded in an interface
// (i.e. fields with no Names — anonymous embedding). Cross-package embeddings
// like `metadata.Foo` get serialized via exprString.
func embeddedTypeNames(iface *ast.InterfaceType) []string {
	var names []string
	if iface.Methods == nil {
		return names
	}
	for _, field := range iface.Methods.List {
		if len(field.Names) != 0 {
			continue // direct method declaration, not embedding
		}
		names = append(names, exprString(field.Type))
	}
	return names
}

// directMethodNames returns the names of methods declared directly on an
// interface (i.e. fields with Names). Embedded sub-interfaces are excluded.
func directMethodNames(iface *ast.InterfaceType) []string {
	var names []string
	if iface.Methods == nil {
		return names
	}
	for _, field := range iface.Methods.List {
		for _, ident := range field.Names {
			names = append(names, ident.Name)
		}
	}
	return names
}

// isBlankIdentList reports whether the var spec uses a single `_` blank
// identifier (the canonical compile-time interface check pattern).
func isBlankIdentList(names []*ast.Ident) bool {
	if len(names) != 1 {
		return false
	}
	return names[0].Name == "_"
}

// targetsBaseCellNilPtr reports whether the var spec value list is exactly
// `(*BaseCell)(nil)`.
func targetsBaseCellNilPtr(values []ast.Expr) bool {
	if len(values) != 1 {
		return false
	}
	call, ok := values[0].(*ast.CallExpr)
	if !ok || len(call.Args) != 1 {
		return false
	}
	// arg must be the identifier `nil`
	argIdent, ok := call.Args[0].(*ast.Ident)
	if !ok || argIdent.Name != "nil" {
		return false
	}
	// callee must be a parenthesized *BaseCell — `(*BaseCell)`
	paren, ok := call.Fun.(*ast.ParenExpr)
	if !ok {
		return false
	}
	star, ok := paren.X.(*ast.StarExpr)
	if !ok {
		return false
	}
	ident, ok := star.X.(*ast.Ident)
	if !ok {
		return false
	}
	return ident.Name == "BaseCell"
}

// equalStringSlices compares two sorted string slices for exact equality.
func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// expectedMethodSetsSHA256 freezes the (sub-interface → method-set) contract
// derived from kernel/cell/interfaces.go source AST. The hash is computed by
// TestCellIfaceISP00_MethodSetsHashGuard at test time by loading the actual
// interface declarations — not from hand-written expected data.
//
// Modifying any sub-interface method set in kernel/cell/interfaces.go requires:
//  1. New ADR amending 202605101800 §D1 with rationale
//  2. Re-running `go test ./tools/archtest/... -run TestCellIfaceISP00` once;
//     copy the "got" value printed by the failure into this constant.
//
// AI-rebust rating upgrade: Hard (source-driven hash — silent modification of
// kernel/cell/interfaces.go is impossible without triggering a hash mismatch;
// prior hand-crafted-data hash only caught drift in the expected tables, not
// in the source).
const expectedMethodSetsSHA256 = "a2cf7188a2b0744897b672580bfc4df6e2e37f0ebc904a2428a4a38f829c90c7"

// INVARIANT: CELL-IFACE-ISP-METHODSETS-01 (hash guard companion)
//
// TestCellIfaceISP00_MethodSetsHashGuard pins the (sub-interface → method-set)
// contract to a SHA-256 digest derived from the actual kernel/cell/interfaces.go
// source AST. Any modification to the 4 sub-interface declarations triggers a
// test failure until the constant is updated, forcing an ADR amendment and
// reviewer attention.
//
// The hash is computed from real source — not from hand-crafted expected data —
// so AI cannot bypass this test by only modifying the expected tables while
// leaving kernel/cell/interfaces.go untouched.
//
// AI-rebust rating: Hard (source-driven hash — silent modification impossible).
func TestCellIfaceISP00_MethodSetsHashGuard(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	// Build the (sub-interface → method-set) map by reading the actual AST.
	liveMethodSets := make(map[string][]string, len(expectedSubInterfaces))
	for _, name := range expectedSubInterfaces {
		iface := loadInterfaceType(t, root, name)
		if iface == nil {
			t.Fatalf("CELL-IFACE-ISP-METHODSETS-01: sub-interface %s not found in kernel/cell/interfaces.go — "+
				"cannot compute source-driven hash", name)
		}
		liveMethodSets[name] = directMethodNames(iface)
	}

	got := computeMethodSetsHash(expectedSubInterfaces, liveMethodSets)
	if got != expectedMethodSetsSHA256 {
		t.Errorf("CELL-IFACE-ISP-METHODSETS-01: kernel/cell/interfaces.go sub-interface method sets changed.\n"+
			"  got      = %s\n"+
			"  expected = %s\n"+
			"Modifying the 4 sub-interface method sets requires:\n"+
			"  1. New ADR amending docs/architecture/202605101800-adr-cell-interface-isp-split.md §D1\n"+
			"  2. Update expectedMethodSetsSHA256 in tools/archtest/cell_iface_isp_invariants_test.go to: %s",
			got, expectedMethodSetsSHA256, got)
	}
}

// computeMethodSetsHash serializes (sub-interface → sorted method names) to a
// deterministic canonical form and returns its SHA-256 hex digest.
func computeMethodSetsHash(ifaces []string, methods map[string][]string) string {
	sortedIfaces := append([]string(nil), ifaces...)
	sort.Strings(sortedIfaces)
	var sb strings.Builder
	for _, name := range sortedIfaces {
		sb.WriteString(name)
		sb.WriteString("={")
		ms := append([]string(nil), methods[name]...)
		sort.Strings(ms)
		sb.WriteString(strings.Join(ms, ","))
		sb.WriteString("};")
	}
	h := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(h[:])
}
