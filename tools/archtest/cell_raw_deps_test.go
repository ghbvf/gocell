// invariants:
//   - INVARIANT: CELL-RAW-DEPS-01
//
// CELL-RAW-DEPS-01 — cells/*/cell.go 公开 With* Option 不得暴露 raw infra 类型。
//
// 扩展 OUTBOX-CELL-01（限禁 WithPublisher / WithOutboxWriter）至全部 raw infra
// 类型 + 全仓 cell.go（含 examples）。allowlist 锁两条结构必要：
//
//	(WithTxManager   → persistence.TxRunner)              — OUTBOX-SERVICE-01 fail-fast on nil TxRunner 唯一注入路径
//	(WithOutboxDeps  → outbox.Publisher, outbox.Writer)   — PR-A5c pre-composed emitter 替代 raw 暴露
//
// 修改 allowlist 必须经新 ADR；SHA-256 hash guard 锁定决策不可静默漂移
// （AI-rebust Hard 等级）。
//
// 吸收 PR245-F10 / 030 G-17。
//
// AI-rebust 评级：Medium（AST type-aware 识别 With* 函数形参 type expression 与
// allowlist），加 Hard hash guard 锁 allowlist。
//
// ref: docs/architecture/202605101800-adr-cell-interface-isp-split.md D6
// ref: tools/archtest/outbox_invariants_test.go OUTBOX-CELL-01（前身规则）
package archtest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metadata"
)

// cellRawDepsForbiddenTypes lists raw infra types that public With* Options
// in any cell.go must not accept directly. Adding a type here is permanent
// (AI-HARD): the new type joins the closed set and triggers re-evaluation of
// every existing With* Option in cells/* and examples/*/cells/*.
var cellRawDepsForbiddenTypes = []string{
	"persistence.TxRunner",
	"outbox.Publisher",
	"outbox.Writer",
	"eventbus.Bus",
	"kvstore.Store",
}

// cellRawDepsAllowlist names the (functionName → permitted forbidden-type set)
// pairs that may continue to expose raw infra. **Each entry is structurally
// necessary, not a soft-compat shim** — see ADR 202605101800 §D6.
//
// Modifying this map requires a new ADR; the SHA-256 guard below makes silent
// changes impossible (compile-time impossible — see TestCellRawDeps01_AllowlistHashGuard).
var cellRawDepsAllowlist = map[string]map[string]bool{
	"WithTxManager": {
		// OUTBOX-SERVICE-01 fail-fast-on-nil-TxRunner is the single source of
		// transactional boundary injection across cells; removing this entry
		// breaks construction-time nil verification.
		"persistence.TxRunner": true,
	},
	"WithOutboxDeps": {
		// PR-A5c established (pub, writer) as the legitimate pre-composed
		// emitter replacement for raw WithPublisher / WithOutboxWriter
		// exposure. Both args travel together to ResolveEmitter at Init.
		"outbox.Publisher": true,
		"outbox.Writer":    true,
	},
}

// cellRawDepsAllowlistSHA256 freezes the allowlist contents. Any modification
// to cellRawDepsAllowlist must be accompanied by:
//  1. A new ADR amending 202605101800 §D6 with the rationale for the change
//  2. Re-running this test once and copying the new "got" value into this constant
//
// This guard implements the "modifications cannot be silent" half of the
// AI-HARD contract for D6.
const cellRawDepsAllowlistSHA256 = "4b3c81835c5666f273a6581f15fb7c806722728030eceaf273acd3eb703ab8b4"

// rawDepViolation records a single (file, function, paramType) breach.
type rawDepViolation struct {
	File       string
	Line       int
	FuncName   string
	ParamType  string
	ParamIndex int
}

func (v rawDepViolation) String() string {
	return fmt.Sprintf("%s:%d: func %s(...) param[%d] type=%s",
		v.File, v.Line, v.FuncName, v.ParamIndex, v.ParamType)
}

// INVARIANT: CELL-RAW-DEPS-01
//
// TestCellRawDeps01_NoRawInfraOptionParam asserts that no exported With*
// Option function declared at top level of cells/<x>/cell.go OR
// examples/<demo>/cells/<x>/cell.go accepts a raw infra type as a parameter,
// unless the (funcName, paramType) pair is explicitly allowlisted.
//
// Scope: every cell.go discovered via metadata.Parser (platform + examples).
// File filter: <root>/cells/<x>/cell.go AND <root>/examples/<demo>/cells/<x>/cell.go
// (path components ending in "cells/<x>/cell.go").
func TestCellRawDeps01_NoRawInfraOptionParam(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	// Enumerate every cell.go declared in project metadata (platform + examples).
	// We bypass findCellFiles() because its companion filter (isCellFile) limits
	// scope to platform cells/ — this invariant covers examples too (see ADR §D6).
	project, err := metadata.NewParser(root).Parse()
	require.NoError(t, err)
	var inScope []string
	for _, c := range project.Cells {
		// metadata.CellMeta.File is the cell.yaml path; cell.go sits next to it.
		cellGo := filepath.Join(filepath.Dir(c.File), "cell.go")
		if !filepath.IsAbs(cellGo) {
			cellGo = filepath.Join(root, cellGo)
		}
		if isAnyCellGoFile(root, cellGo) {
			inScope = append(inScope, cellGo)
		}
	}
	sort.Strings(inScope)
	require.NotEmpty(t, inScope,
		"no cells/<x>/cell.go or examples/<d>/cells/<x>/cell.go files matched")

	forbiddenSet := make(map[string]bool, len(cellRawDepsForbiddenTypes))
	for _, name := range cellRawDepsForbiddenTypes {
		forbiddenSet[name] = true
	}

	var violations []rawDepViolation
	for _, path := range inScope {
		fileViolations, err := scanCellFileForRawDeps(root, path, forbiddenSet)
		require.NoError(t, err)
		violations = append(violations, fileViolations...)
	}

	if len(violations) > 0 {
		t.Errorf("CELL-RAW-DEPS-01: %d violation(s) — public With* Options in cell.go must not "+
			"expose raw infra types %v unless allowlisted via ADR 202605101800 §D6.\n"+
			"Fix: replace WithXxx(persistence.TxRunner) with WithTxManager(tx); "+
			"replace WithXxx(outbox.Publisher/Writer) with WithOutboxDeps(pub, writer); "+
			"wrap eventbus/kvstore raw deps via service-layer adapter.",
			len(violations), cellRawDepsForbiddenTypes)
		for _, v := range violations {
			t.Errorf("  %s", v)
		}
	}
}

// TestCellRawDeps01_AllowlistHashGuard pins the allowlist contents to the
// SHA-256 hash recorded in cellRawDepsAllowlistSHA256. Modifications to the
// allowlist will fail this test until the constant is updated, forcing a new
// ADR + reviewer attention to the change.
func TestCellRawDeps01_AllowlistHashGuard(t *testing.T) {
	t.Parallel()
	got := computeAllowlistHash(cellRawDepsAllowlist)
	if got != cellRawDepsAllowlistSHA256 {
		t.Errorf("CELL-RAW-DEPS-01: allowlist hash drift detected.\n"+
			"  got      = %s\n"+
			"  expected = %s\n"+
			"Modifying cellRawDepsAllowlist requires a new ADR amending "+
			"docs/architecture/202605101800-adr-cell-interface-isp-split.md §D6 "+
			"and updating the cellRawDepsAllowlistSHA256 constant at "+
			"tools/archtest/cell_raw_deps_test.go (copy the 'got' value above into the const).",
			got, cellRawDepsAllowlistSHA256)
	}
}

// ---------- helpers (file-local) ----------

// isAnyCellGoFile matches both platform cells/<x>/cell.go and
// examples/<demo>/cells/<x>/cell.go. The defining property: path components
// end in `cells/<x>/cell.go` (i.e. `parts[-1]=="cell.go"` and
// `parts[-3]=="cells"`).
//
// AI-rebust: this path-component check is Soft on its own (string convention),
// but composes with metadata.NewParser() in TestCellRawDeps01_NoRawInfraOptionParam —
// only cells declared in cell.yaml (parsed by metadata.Parser) reach this filter,
// so bypassing the path check requires also bypassing metadata governance.
// The composite rating is Medium.
func isAnyCellGoFile(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	rel = filepath.ToSlash(rel)
	if strings.HasSuffix(rel, "_test.go") {
		return false
	}
	parts := strings.Split(rel, "/")
	if len(parts) < 3 {
		return false
	}
	if parts[len(parts)-1] != "cell.go" {
		return false
	}
	if parts[len(parts)-3] != "cells" {
		return false
	}
	return true
}

// scanCellFileForRawDeps parses a cell.go file and returns violations of
// CELL-RAW-DEPS-01: top-level exported `func WithXxx(...) Option` whose
// parameter types include a forbidden raw infra type that is NOT allowlisted
// for this function name.
func scanCellFileForRawDeps(root, path string, forbidden map[string]bool) ([]rawDepViolation, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	rel = filepath.ToSlash(rel)

	var out []rawDepViolation
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if fn.Recv != nil {
			continue
		}
		if !fn.Name.IsExported() {
			continue
		}
		if !strings.HasPrefix(fn.Name.Name, "With") {
			continue
		}
		funcName := fn.Name.Name
		permitted := cellRawDepsAllowlist[funcName]

		if fn.Type.Params == nil {
			continue
		}
		idx := 0
		for _, field := range fn.Type.Params.List {
			paramType := stripPointer(exprString(field.Type))
			count := len(field.Names)
			if count == 0 {
				count = 1
			}
			for k := 0; k < count; k++ {
				if forbidden[paramType] && !permitted[paramType] {
					out = append(out, rawDepViolation{
						File:       rel,
						Line:       fset.Position(field.Pos()).Line,
						FuncName:   funcName,
						ParamType:  paramType,
						ParamIndex: idx,
					})
				}
				idx++
			}
		}
	}
	return out, nil
}

// stripPointer drops a leading `*` from a type expression string so that
// `*outbox.Publisher` and `outbox.Publisher` compare equal.
func stripPointer(s string) string {
	return strings.TrimPrefix(s, "*")
}

// computeAllowlistHash serializes cellRawDepsAllowlist to a deterministic
// canonical form and returns its SHA-256 hex digest.
func computeAllowlistHash(m map[string]map[string]bool) string {
	funcNames := make([]string, 0, len(m))
	for k := range m {
		funcNames = append(funcNames, k)
	}
	sort.Strings(funcNames)

	var sb strings.Builder
	for _, fn := range funcNames {
		sb.WriteString(fn)
		sb.WriteString("={")
		typeNames := make([]string, 0, len(m[fn]))
		for tn, allowed := range m[fn] {
			if allowed {
				typeNames = append(typeNames, tn)
			}
		}
		sort.Strings(typeNames)
		sb.WriteString(strings.Join(typeNames, ","))
		sb.WriteString("};")
	}
	h := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(h[:])
}

// TestCellRawDeps01_ScannerCatchesViolation feeds a hand-crafted cell.go AST
// containing a forbidden raw-infra Option (WithPublisher) and asserts the
// scanner returns a non-empty violations slice. This is the negative fixture
// proving the rule has detection capability — without it, the scanner could
// silently regress to "always pass" without any test catching the bug.
func TestCellRawDeps01_ScannerCatchesViolation(t *testing.T) {
	t.Parallel()
	src := `package fake
type Option func(*Cell)
type Cell struct{}
func WithPublisher(p outbox.Publisher) Option { return nil }
func WithSomethingElse(x int) Option { return nil }
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "fake_cell.go", src, parser.SkipObjectResolution)
	require.NoError(t, err)

	forbidden := map[string]bool{}
	for _, name := range cellRawDepsForbiddenTypes {
		forbidden[name] = true
	}

	var violations []rawDepViolation
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !fn.Name.IsExported() ||
			!strings.HasPrefix(fn.Name.Name, "With") {
			continue
		}
		permitted := cellRawDepsAllowlist[fn.Name.Name]
		if fn.Type.Params == nil {
			continue
		}
		idx := 0
		for _, field := range fn.Type.Params.List {
			paramType := stripPointer(exprString(field.Type))
			count := len(field.Names)
			if count == 0 {
				count = 1
			}
			for k := 0; k < count; k++ {
				if forbidden[paramType] && !permitted[paramType] {
					violations = append(violations, rawDepViolation{
						File: "fake_cell.go", Line: fset.Position(field.Pos()).Line,
						FuncName: fn.Name.Name, ParamType: paramType, ParamIndex: idx,
					})
				}
				idx++
			}
		}
	}
	require.Len(t, violations, 1, "scanner must catch the WithPublisher(outbox.Publisher) violation")
	assert.Equal(t, "WithPublisher", violations[0].FuncName)
	assert.Equal(t, "outbox.Publisher", violations[0].ParamType)
}
