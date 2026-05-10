// invariants:
//   - INVARIANT: CELL-RAW-DEPS-01
//
// CELL-RAW-DEPS-01 — cells/*/cell.go 公开 With* Option 不得暴露 raw infra 类型。
//
// Scanner 升级至 L4 type-aware（ai-collab.md §L4）：使用
// typeseval.SharedResolver + types.Info canonical type resolution，
// 抵抗 import alias / type alias / hand-crafted AST bypass。
// AI-rebust 评级：Hard（type system + canonical package path + path-precise allowlist）。
//
// Allowlist 从 (funcName → type) 二元组升级至
// (file_pattern, funcName, canonicalType) 三元组，按 cell 真实能力精确建模：
//
//   - Platform cells (cells/*): WithTxManager + WithOutboxDeps(pub, writer)
//   - ordercell L2: WithTxManager + WithOutboxWriter(writer) [无 publisher 路径]
//   - devicecell L4: WithDirectPublisher(pub) [无 writer，无 txRunner]
//
// 修改 allowlist 必须经新 ADR amending 202605101800 §D6；
// SHA-256 hash guard 锁定决策不可静默漂移。
//
// ref: docs/architecture/202605101800-adr-cell-interface-isp-split.md D6
// ref: tools/archtest/outbox_invariants_test.go OUTBOX-CELL-01（前身规则）
package archtest

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/kernel/metadata"
	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
)

// cellRawDepsForbiddenTypes lists raw infra types by canonical package path
// ("pkgpath.TypeName") that public With* Options in any cell.go must not
// accept directly. Canonical paths are resolved by go/types — immune to
// import alias and type alias attacks.
//
// eventbus.Bus / kvstore.Store are absent: these packages do not exist as
// standalone types in the kernel layer (eventbus is runtime/, kvstore has no
// kernel interface). Adding a type here is permanent (AI-HARD): it triggers
// re-evaluation of every existing With* Option.
var cellRawDepsForbiddenTypes = []string{
	"github.com/ghbvf/gocell/kernel/persistence.TxRunner",
	"github.com/ghbvf/gocell/kernel/outbox.Publisher",
	"github.com/ghbvf/gocell/kernel/outbox.Writer",
}

// rawDepsAllowEntry models the exact (file glob, exported function name,
// canonical type) triple that may expose a raw infra type. Each entry
// reflects the cell's REAL capability — not a generic name-based pass.
//
// PathPattern uses filepath.Match semantics (OS path separator):
// "cells/*/cell.go" matches all platform cells.
type rawDepsAllowEntry struct {
	PathPattern string // filepath.Match glob relative to module root
	FuncName    string // exported With* function name
	ParamType   string // canonical "pkgpath.TypeName"
}

// cellRawDepsAllowlist enumerates the (file glob, func, canonical type) triples
// that may expose raw infra. Each entry models the cell's REAL capability —
// not a uniform signature imposed for historical uniformity.
//
// Modifying this list requires a new ADR amending
// docs/architecture/202605101800-adr-cell-interface-isp-split.md §D6 with
// rationale; the SHA-256 hash guard makes silent changes impossible.
var cellRawDepsAllowlist = []rawDepsAllowEntry{
	// Platform cells (cells/{accesscore,auditcore,configcore}/cell.go) — L1/L2:
	// pre-composed emitter via (pub, writer) pair + transactional outbox.
	{"cells/*/cell.go", "WithTxManager", "github.com/ghbvf/gocell/kernel/persistence.TxRunner"},
	{"cells/*/cell.go", "WithOutboxDeps", "github.com/ghbvf/gocell/kernel/outbox.Publisher"},
	{"cells/*/cell.go", "WithOutboxDeps", "github.com/ghbvf/gocell/kernel/outbox.Writer"},

	// ordercell L2 OutboxFact: writer + tx manager only.
	// No publisher path — ordercell deliberately omits MetricsProvider/Clock
	// wiring required for a DirectEmitter. CELL-RAW-DEPS-01 enforces this
	// statically, replacing the deleted MustHaveNilOrderCellPublisher panic guard.
	{"examples/todoorder/cells/ordercell/cell.go", "WithTxManager", "github.com/ghbvf/gocell/kernel/persistence.TxRunner"},
	{"examples/todoorder/cells/ordercell/cell.go", "WithOutboxWriter", "github.com/ghbvf/gocell/kernel/outbox.Writer"},

	// devicecell L4 DeviceLatent: direct publish only (no writer, no txRunner).
	// Replaced the deleted MustHaveNilDeviceCellWriter panic guard.
	{"examples/iotdevice/cells/devicecell/cell.go", "WithDirectPublisher", "github.com/ghbvf/gocell/kernel/outbox.Publisher"},
}

// cellRawDepsAllowlistSHA256 freezes the allowlist contents. Any modification
// to cellRawDepsAllowlist must be accompanied by:
//  1. A new ADR amending 202605101800 §D6 with the rationale for the change
//  2. Re-running this test once and copying the new "got" value into this constant
//
// This guard implements the "modifications cannot be silent" half of the
// AI-HARD contract for D6.
const cellRawDepsAllowlistSHA256 = "7631badf92149c08bc5e7653e50d00bd6cc221104ba8713799c4ee2a6703222c"

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
// unless the (file, funcName, canonicalType) triple is explicitly allowlisted.
//
// Scanner: type-aware via typeseval.SharedResolver + types.Info canonical type
// resolution (AI-rebust L4 per ai-collab.md). Defeats import alias, type alias,
// and hand-crafted AST bypass attacks.
//
// Scope: every cell.go discovered via metadata.Parser (platform + examples).
func TestCellRawDeps01_NoRawInfraOptionParam(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	// Enumerate every cell.go declared in project metadata (platform + examples).
	project, err := metadata.NewParser(root).Parse()
	require.NoError(t, err)

	var patterns []string
	for _, c := range project.Cells {
		cellDir := filepath.Dir(c.File)
		if !filepath.IsAbs(cellDir) {
			cellDir = filepath.Join(root, cellDir)
		}
		rel, relErr := filepath.Rel(root, cellDir)
		require.NoError(t, relErr)
		patterns = append(patterns, "./"+filepath.ToSlash(rel))
	}
	require.NotEmpty(t, patterns, "no cells discovered via metadata.Parser")

	resolver, err := typeseval.SharedResolver(root, false, nil, patterns...)
	require.NoError(t, err)

	forbiddenSet := make(map[string]bool, len(cellRawDepsForbiddenTypes))
	for _, n := range cellRawDepsForbiddenTypes {
		forbiddenSet[n] = true
	}

	var violations []rawDepViolation
	for _, pkg := range resolver.Packages() {
		if pkg.TypesInfo == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			absPath := pkg.Fset.Position(file.Pos()).Filename
			if !isAnyCellGoFile(root, absPath) {
				continue
			}
			rel, relErr := filepath.Rel(root, absPath)
			if relErr != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			violations = append(violations,
				scanFileForRawDeps(pkg.Fset, pkg.TypesInfo, file, rel, forbiddenSet)...)
		}
	}

	if len(violations) > 0 {
		t.Errorf("CELL-RAW-DEPS-01: %d violation(s) — public With* Options in cell.go must not "+
			"expose raw infra types %v unless allowlisted via ADR 202605101800 §D6.\n"+
			"Fix: replace WithXxx(persistence.TxRunner) with WithTxManager(tx); "+
			"replace WithXxx(outbox.Writer) with the cell-specific Option matching its real capability; "+
			"replace WithXxx(outbox.Publisher) with WithDirectPublisher(pub) for L4 cells.",
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

// TestCellRawDeps01_ScannerCatchesViolation loads the real fixture package
// tools/archtest/internal/rawdepfixture via typeseval.SharedResolver and
// asserts the scanner detects exactly one CELL-RAW-DEPS-01 violation.
//
// This is the negative fixture proving the scanner has detection capability —
// per ai-collab.md §L4 "real source AST capture (AI 难造假)": the fixture is
// a genuine Go package loaded via packages.Load, not a hand-crafted AST.
// Bypassing this test requires modifying real source code, not crafting a
// synthetic AST node.
func TestCellRawDeps01_ScannerCatchesViolation(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	fixturePkg := "./tools/archtest/internal/rawdepfixture"

	resolver, err := typeseval.SharedResolver(root, false, nil, fixturePkg)
	require.NoError(t, err)

	forbiddenSet := make(map[string]bool, len(cellRawDepsForbiddenTypes))
	for _, n := range cellRawDepsForbiddenTypes {
		forbiddenSet[n] = true
	}

	var totalViolations []rawDepViolation
	for _, pkg := range resolver.Packages() {
		if pkg.TypesInfo == nil {
			continue
		}
		for _, file := range pkg.Syntax {
			absPath := pkg.Fset.Position(file.Pos()).Filename
			rel, relErr := filepath.Rel(root, absPath)
			if relErr != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			// Fixture is NOT under cells/<x>/cell.go — bypass isAnyCellGoFile
			// and scan all files in the fixture package directly.
			totalViolations = append(totalViolations,
				scanFileForRawDeps(pkg.Fset, pkg.TypesInfo, file, rel, forbiddenSet)...)
		}
	}

	require.Len(t, totalViolations, 1,
		"fixture must contain exactly one CELL-RAW-DEPS-01 violation (WithEvilTxManager)")
	assert.Equal(t, "WithEvilTxManager", totalViolations[0].FuncName)
	assert.Equal(t, "github.com/ghbvf/gocell/kernel/persistence.TxRunner", totalViolations[0].ParamType)
}

// TestCellRawDeps01_AllowlistChangeWorkflow verifies the hash guard's
// detection capability by computing the SHA-256 of four mutated clones of
// the allowlist and asserting each clone's hash differs from the canonical.
//
// Without this test, the hash guard could silently regress (e.g. a buggy
// serialiser that ignores deletions) and only fail when a real attacker
// exploits the gap. This test covers all four mutation classes:
//
//   - add new entry
//   - modify existing PathPattern
//   - modify existing ParamType
//   - delete existing entry
func TestCellRawDeps01_AllowlistChangeWorkflow(t *testing.T) {
	t.Parallel()
	canonical := computeAllowlistHash(cellRawDepsAllowlist)

	type mutation struct {
		name   string
		mutate func([]rawDepsAllowEntry) []rawDepsAllowEntry
	}
	mutations := []mutation{
		{
			name: "add new entry",
			mutate: func(entries []rawDepsAllowEntry) []rawDepsAllowEntry {
				return append(entries, rawDepsAllowEntry{
					"cells/*/cell.go",
					"WithVaultClient",
					"github.com/example/vault.Client",
				})
			},
		},
		{
			name: "modify PathPattern of existing entry",
			mutate: func(entries []rawDepsAllowEntry) []rawDepsAllowEntry {
				clone := cloneAllowlist(entries)
				clone[0].PathPattern = "cells/accesscore/cell.go"
				return clone
			},
		},
		{
			name: "modify ParamType of existing entry",
			mutate: func(entries []rawDepsAllowEntry) []rawDepsAllowEntry {
				clone := cloneAllowlist(entries)
				clone[0].ParamType = "github.com/ghbvf/gocell/kernel/persistence.TxManager"
				return clone
			},
		},
		{
			name: "delete existing entry",
			mutate: func(entries []rawDepsAllowEntry) []rawDepsAllowEntry {
				clone := cloneAllowlist(entries)
				return clone[:len(clone)-1]
			},
		},
	}

	for _, mut := range mutations {
		t.Run(mut.name, func(t *testing.T) {
			t.Parallel()
			mutated := mut.mutate(cellRawDepsAllowlist)
			mutatedHash := computeAllowlistHash(mutated)
			if mutatedHash == canonical {
				t.Errorf("CELL-RAW-DEPS-01: allowlist mutation %q produced same hash %s; "+
					"hash guard fails to detect this mutation class — ADR §D6 broken",
					mut.name, mutatedHash)
			}
		})
	}
}

// ---------- helpers ----------

// canonicalTypeName returns "<pkgpath>.<name>" for any type expression,
// resolving import aliases, type aliases, and pointer wrapping via go/types.
// Returns "" when the expression cannot be resolved.
func canonicalTypeName(info *types.Info, expr ast.Expr) string {
	if info == nil {
		return ""
	}
	tv, ok := info.Types[expr]
	if !ok || tv.Type == nil {
		return ""
	}
	t := tv.Type
	// Strip pointer indirection.
	for {
		ptr, ok := t.(*types.Pointer)
		if !ok {
			break
		}
		t = ptr.Elem()
	}
	if named, ok := t.(*types.Named); ok {
		obj := named.Obj()
		if obj.Pkg() == nil {
			return obj.Name()
		}
		return obj.Pkg().Path() + "." + obj.Name()
	}
	return t.String()
}

// scanFileForRawDeps scans a single parsed file for CELL-RAW-DEPS-01
// violations using type-aware canonical name resolution.
func scanFileForRawDeps(fset *token.FileSet, info *types.Info, file *ast.File, rel string, forbidden map[string]bool) []rawDepViolation {
	var out []rawDepViolation
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || !fn.Name.IsExported() ||
			!strings.HasPrefix(fn.Name.Name, "With") {
			continue
		}
		if fn.Type.Params == nil {
			continue
		}
		funcName := fn.Name.Name
		idx := 0
		for _, field := range fn.Type.Params.List {
			canonical := canonicalTypeName(info, field.Type)
			count := len(field.Names)
			if count == 0 {
				count = 1
			}
			for k := 0; k < count; k++ {
				if forbidden[canonical] && !isRawDepsAllowed(rel, funcName, canonical) {
					out = append(out, rawDepViolation{
						File:       rel,
						Line:       fset.Position(field.Pos()).Line,
						FuncName:   funcName,
						ParamType:  canonical,
						ParamIndex: idx,
					})
				}
				idx++
			}
		}
	}
	return out
}

// isRawDepsAllowed returns true when the (filePath, funcName, canonicalType)
// triple is authorized by cellRawDepsAllowlist.
func isRawDepsAllowed(filePath, funcName, canonicalType string) bool {
	for _, e := range cellRawDepsAllowlist {
		if e.FuncName != funcName || e.ParamType != canonicalType {
			continue
		}
		ok, _ := filepath.Match(filepath.FromSlash(e.PathPattern), filepath.FromSlash(filePath))
		if ok {
			return true
		}
	}
	return false
}

// computeAllowlistHash serializes cellRawDepsAllowlist to a deterministic
// canonical form and returns its SHA-256 hex digest.
func computeAllowlistHash(entries []rawDepsAllowEntry) string {
	sorted := cloneAllowlist(entries)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].PathPattern != sorted[j].PathPattern {
			return sorted[i].PathPattern < sorted[j].PathPattern
		}
		if sorted[i].FuncName != sorted[j].FuncName {
			return sorted[i].FuncName < sorted[j].FuncName
		}
		return sorted[i].ParamType < sorted[j].ParamType
	})
	var sb strings.Builder
	for _, e := range sorted {
		sb.WriteString(e.PathPattern)
		sb.WriteByte('|')
		sb.WriteString(e.FuncName)
		sb.WriteByte('|')
		sb.WriteString(e.ParamType)
		sb.WriteByte(';')
	}
	h := sha256.Sum256([]byte(sb.String()))
	return hex.EncodeToString(h[:])
}

// cloneAllowlist deep-copies the allowlist slice so per-test mutations
// do not leak across test cases.
func cloneAllowlist(entries []rawDepsAllowEntry) []rawDepsAllowEntry {
	out := make([]rawDepsAllowEntry, len(entries))
	copy(out, entries)
	return out
}

// isAnyCellGoFile matches both platform cells/<x>/cell.go and
// examples/<demo>/cells/<x>/cell.go. The defining property: path components
// end in `cells/<x>/cell.go` (parts[-1]=="cell.go" and parts[-3]=="cells").
//
// AI-rebust: path-component check composes with metadata.NewParser() in
// TestCellRawDeps01_NoRawInfraOptionParam — only cells declared in cell.yaml
// reach this filter, so bypassing the path check also requires bypassing
// metadata governance. Composite rating: Medium.
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
