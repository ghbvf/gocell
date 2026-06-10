//go:build archtest

// INVARIANT: NO-MANUAL-CONTRACTSPEC-LITERAL-01
//
// # NO-MANUAL-CONTRACTSPEC-LITERAL-01
//
// Invariant: contractspec.ContractSpec{…} composite literals and
// contractspec.EventSpec(…) call expressions must only appear in:
//   - generated/contracts/**/*_gen.go — business contract specs (codegen output)
//   - kernel/contractspec/** itself  — the ContractSpec type definition
//
// Hand-written production code across EVERY non-test production tree that can
// import kernel/contractspec — cells/, examples/, runtime/, kernel/, cmd/,
// adapters/, cellmodules/ (see productionScanDirs) — must NOT define
// ContractSpec literals. The scope is intentionally repo-wide (not just
// cells/+examples-cells/+runtime/) so the "non-runtime code cannot hand-write a
// framework ContractSpec" claim in runtime/internal/contractbuild/doc.go holds
// for cmd/, adapters/, kernel/, cellmodules/ too (#1038 review C1/F2).
// Framework-owned HTTP infra (health probes, devtools catalog) and event-tracing
// derivations use the typed funnels NewFrameworkHTTP / NewEventDerivation in
// runtime/internal/contractbuild (issue #1038 moved them there from
// kernel/contractspec — see below).
//
// Funnel home + upstream gate (issue #1038): both funnels live in
// runtime/internal/contractbuild. Because that package sits under
// runtime/internal/, the Go compiler refuses imports from outside the runtime/
// subtree (cells/, examples/, cmd/, adapters/, kernel/, tools/, tests/), so
// business code physically cannot call them — a compiler-Hard upstream gate.
// This is the same Medium→Hard upgrade issue #638 applied to
// runtime/internal/authtest (ai-robust.md §Hard 范本目录 → "internal/ wrap 包").
// The funnels construct ContractSpec{…} literals, so contractbuild is excluded
// from this scan (the sanctioned funnel home, analogous to kernel/contractspec).
//
// Exclusions:
//   - generated/contracts/**/*_gen.go     — the authoritative home for business contracts
//   - tools/codegen/**/testdata/**        — codegen fixture files
//   - **/fixtures/**                      — test fixture trees
//   - kernel/contractspec/** itself       — defines the ContractSpec type
//   - runtime/internal/contractbuild/**   — the sanctioned runtime-side funnel home
//   - *_test.go                           — test helpers may reference specs for assertions
//
// AI-robust:
//   - Composite-literal ban: Hard (downstream) — `contractspec.ContractSpec{…}`
//     under every production tree in productionScanDirs (cells/ + examples/ +
//     runtime/ + kernel/ + cmd/ + adapters/ + cellmodules/) is unrepresentable
//     (archtest fails CI), the typed funnels are the only surviving form.
//   - NewFrameworkHTTP upstream: Hard — runtime/internal/ placement; non-runtime
//     callers are a compile error. Content Hard via frameworkHTTPIDPrefix panic.
//   - NewEventDerivation upstream: caller-set Hard — same runtime/internal/
//     placement (non-runtime callers are a compile error). Content is shape-Hard,
//     NOT provenance-Hard: sub.Validate() inside the funnel guarantees the spec is
//     never structurally invalid, but outbox.Subscription has exported fields so a
//     runtime/ caller can fabricate one from arbitrary strings — the typed param
//     forces shape, it does NOT type-enforce value provenance. Real provenance is
//     the eventrouter data flow, Hard-ifiable only by a sealed Subscription
//     constructor (gh #1532). The former single-file caller allowlist
//     (eventDerivationAllowedCaller) + drift guard are RETIRED (#1038 / #1445):
//     the typed parameter became natural once the funnel moved to runtime/ (it may
//     now import kernel/outbox) — that signature is a shape gate, not a provenance
//     gate. See runtime/internal/contractbuild/doc.go for the single-source grading.
//
// Aligns with the "typed function call as Hard funnel for unbounded
// operations" charter pattern (PANIC-REGISTERED-01 same path).
//
// ref: docs/plans/202605011500-029-master-roadmap.md K#PR4 W3 + G-04
// ref: docs/reviews/202605181109-042-archtest-six-agent-audit.md §3b/§4 row 9
// ref: runtime/internal/contractbuild/doc.go (#1038 funnel home + grading)
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// TestNO_MANUAL_CONTRACTSPEC_LITERAL_01 scans production .go files under
// cells/, examples/*/cells/, and runtime/ for contractspec.ContractSpec{…}
// composite literals and contractspec.EventSpec(…) call expressions,
// failing on any found. The typed funnels in runtime/internal/contractbuild
// (#1038) are the only legitimate runtime-side construction paths; that
// package is excluded from the scan as the sanctioned funnel home.
func TestNO_MANUAL_CONTRACTSPEC_LITERAL_01(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	files := collectContractSpecScanFiles(t, root)

	var violations []string
	for _, f := range files {
		rel, _ := filepath.Rel(root, f)
		rel = filepath.ToSlash(rel)
		hits := scanForContractSpecLiterals(token.NewFileSet(), f, rel)
		violations = append(violations, hits...)
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("NO-MANUAL-CONTRACTSPEC-LITERAL-01: %s", v)
	}
}

// TestNO_MANUAL_CONTRACTSPEC_LITERAL_01_ExcludesConstructionHomes pins the
// construction-home exclusions (runtime/internal/contractbuild/ +
// kernel/contractspec/). Both homes legitimately hold contractspec.ContractSpec
// values; if a home prefix drifts (e.g. a missing trailing slash), its files
// re-enter the scan and the main test reds with a confusing "home flagged"
// message. This test asserts each home is excluded directly AND load-bearing
// (the home dir exists with at least one production .go file), so a path typo
// surfaces here with a clear cause.
func TestNO_MANUAL_CONTRACTSPEC_LITERAL_01_ExcludesConstructionHomes(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	collected := collectContractSpecScanFiles(t, root)
	for _, home := range contractSpecConstructionHomes {
		for _, f := range collected {
			rel, _ := filepath.Rel(root, f)
			if strings.HasPrefix(filepath.ToSlash(rel), home) {
				t.Errorf("construction home %q must be excluded from scan, but collected: %s", home, rel)
			}
		}
		// Guard against a vacuous exclusion: the home dir must exist (a typo'd
		// prefix that matches nothing would silently excuse nothing).
		info, err := os.Stat(filepath.Join(root, filepath.FromSlash(home)))
		if err != nil || !info.IsDir() {
			t.Fatalf("construction home %q must exist as a directory for the exclusion to be load-bearing: %v", home, err)
		}
	}
}

// CONTRACTSPEC-FRAMEWORK-BUILDERS-EXIST-01 retired (#1038). The funnels moved
// to runtime/internal/contractbuild, which tools/archtest cannot import (Go
// internal/ rule — tools/ is outside the runtime/ subtree). The compile-time
// existence lock now lives in the real runtime/ callers (runtime/bootstrap,
// runtime/http/devtools, runtime/eventrouter) and contractbuild's own unit
// test: a rename/removal/signature change of either funnel breaks those.

// contractSpecConstructionHomes are the two sanctioned ContractSpec
// construction sites excluded from this scan:
//   - runtime/internal/contractbuild/ — the typed funnels (NewFrameworkHTTP /
//     NewEventDerivation) legitimately build ContractSpec{…} literals here.
//   - kernel/contractspec/ — the type-definition home (it may construct
//     zero-value / helper literals of its own type).
//
// generated/contracts/**/*_gen.go is excluded separately via the _gen.go
// suffix filter (and DirsScope's default "generated" dir skip).
var contractSpecConstructionHomes = []string{
	"runtime/internal/contractbuild/",
	"kernel/contractspec/",
}

// productionScanDirs are the top-level production trees scanned for raw
// ContractSpec{…} literals. The set covers EVERY non-test production tree that
// can import kernel/contractspec — not just cells/ — so the "non-runtime code
// cannot hand-write a framework ContractSpec" claim in
// runtime/internal/contractbuild/doc.go is repo-wide true, not only for
// cells/+examples-cells/+runtime/ (#1038 review C1/F2). DirsScope auto-skips
// vendor / testdata / worktrees / generated / *_test.go.
var productionScanDirs = []string{
	"runtime", "kernel", "cmd", "adapters", "cellmodules", "examples",
}

// collectContractSpecScanFiles returns production .go files to scan.
// Scope: cells (top-level cells/ + examples/*/cells/) discovered via
// findCellProductionGoFiles (metadata-driven), unioned with productionScanDirs
// via DirsScope directory walk. The two construction homes
// (contractSpecConstructionHomes) and *_gen.go files are excluded from the
// unioned set.
func collectContractSpecScanFiles(t *testing.T, root string) []string {
	t.Helper()
	cellFiles, err := findCellProductionGoFiles(root)
	if err != nil {
		t.Fatalf("metadata.NewParser: %v", err)
	}
	dirFiles, err := scanner.DirsScope(root, productionScanDirs).Files()
	if err != nil {
		t.Fatalf("scanner.DirsScope(%v): %v", productionScanDirs, err)
	}
	seen := make(map[string]struct{}, len(cellFiles)+len(dirFiles))
	out := make([]string, 0, len(cellFiles)+len(dirFiles))
	for _, f := range slices.Concat(cellFiles, dirFiles) {
		if strings.HasSuffix(f, "_gen.go") {
			continue
		}
		// Exclude the sanctioned construction homes.
		rel, relErr := filepath.Rel(root, f)
		if relErr == nil {
			relSlash := filepath.ToSlash(rel)
			if slices.ContainsFunc(contractSpecConstructionHomes, func(home string) bool {
				return strings.HasPrefix(relSlash, home)
			}) {
				continue
			}
		}
		if _, dup := seen[f]; dup {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, f)
	}
	sort.Strings(out)
	return out
}

// TestNO_MANUAL_CONTRACTSPEC_LITERAL_01_NegativeFixture verifies that the
// scanner correctly identifies a contractspec.ContractSpec{} literal in a
// hand-written file. The fixture in testdata/no_manual_contractspec_literal/
// contains a deliberate violation.
func TestNO_MANUAL_CONTRACTSPEC_LITERAL_01_NegativeFixture(t *testing.T) {
	t.Parallel()
	fixturePath, err := filepath.Abs(filepath.Join("testdata", "no_manual_contractspec_literal", "violates", "handler.go"))
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	// Simulate the relative path as it would appear under cells/.
	rel := "cells/fake/slices/bad/handler.go"
	violations := scanForContractSpecLiterals(token.NewFileSet(), fixturePath, rel)
	if len(violations) == 0 {
		t.Errorf("expected at least 1 violation for fixture with manual ContractSpec literal, got 0")
	}
	// Verify the violation message is informative.
	for _, v := range violations {
		if !strings.Contains(v, "ContractSpec") {
			t.Errorf("violation message should mention ContractSpec: %q", v)
		}
	}
}

// NewEventDerivation caller-allowlist tests retired (#1038): the funnel moved
// to runtime/internal/contractbuild, where the Go compiler seals non-runtime
// callers; the former single-file ("only eventrouter") allowlist + its drift
// guard guarded only a non-security tracing projection (Validate()-closed) and
// are now pure ceremony. See package doc + runtime/internal/contractbuild/doc.go.

// TestNO_MANUAL_CONTRACTSPEC_LITERAL_01_NegativeFixture_EventSpec verifies that
// the scanner correctly identifies a contractspec.EventSpec() call expression.
// EventSpec does not exist in the real codebase (it is a hypothetical helper),
// so this test uses in-memory source parsing rather than building the source.
func TestNO_MANUAL_CONTRACTSPEC_LITERAL_01_NegativeFixture_EventSpec(t *testing.T) {
	t.Parallel()
	src := `package p
import "github.com/ghbvf/gocell/kernel/contractspec"
func init() {
	_ = contractspec.EventSpec("event.bad.v1", "amqp", "bad.topic")
}
`
	fset := token.NewFileSet()
	_, err := parser.ParseFile(fset, "handler.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	tmp, err := os.CreateTemp(t.TempDir(), "eventspec_test_*.go")
	if err != nil {
		t.Fatalf("create temp: %v", err)
	}
	if _, err := tmp.WriteString(src); err != nil {
		t.Fatalf("write temp: %v", err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatalf("close temp: %v", err)
	}

	violations := scanForContractSpecLiterals(token.NewFileSet(), tmp.Name(), "cells/fake/handler.go")
	if len(violations) == 0 {
		t.Errorf("expected at least 1 violation for contractspec.EventSpec() call, got 0")
	}
	for _, v := range violations {
		if !strings.Contains(v, "EventSpec") {
			t.Errorf("violation message should mention EventSpec: %q", v)
		}
	}
}

// scanForContractSpecLiterals AST-scans f for:
//  1. contractspec.ContractSpec{…} composite literals
//  2. contractspec.EventSpec(…) call expressions
//
// where "contractspec" is the local alias for kernel/contractspec. Both forms
// are forbidden globally outside generated/contracts/**/*_gen.go. The
// NewFrameworkHTTP / NewEventDerivation funnels are NOT scanned here: they
// moved to runtime/internal/contractbuild (#1038), where the Go compiler seals
// non-runtime callers — a compiler-Hard upstream gate that needs no path-string
// allowlist (see package doc).
//
// rel MUST be slash-separated (apply filepath.ToSlash at the caller).
func scanForContractSpecLiterals(fset *token.FileSet, path, rel string) []string {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		return nil
	}
	f, err := parser.ParseFile(fset, path, data, parser.SkipObjectResolution)
	if err != nil {
		return nil // syntax errors handled by build
	}

	alias := contractspecLocalAlias(f)
	if alias == "" {
		return nil // file does not import kernel/contractspec
	}

	var violations []string
	// Match contractspec.ContractSpec{…} composite literals.
	scanner.EachInSubtree[ast.CompositeLit](f, func(node *ast.CompositeLit) {
		sel, ok := node.Type.(*ast.SelectorExpr)
		if !ok {
			return
		}
		ident, ok2 := sel.X.(*ast.Ident)
		if !ok2 || ident.Name != alias || sel.Sel.Name != "ContractSpec" {
			return
		}
		pos := fset.Position(node.Pos())
		violations = append(violations, fmt.Sprintf(
			"%s:%d: manual %s.ContractSpec{} literal — must be in generated/contracts/**/*_gen.go only",
			rel, pos.Line, alias,
		))
	})
	// Match contractspec.EventSpec(…) call expressions — forbidden globally
	// outside generated/contracts/**/*_gen.go.
	scanner.EachInSubtree[ast.CallExpr](f, func(node *ast.CallExpr) {
		sel, ok := node.Fun.(*ast.SelectorExpr)
		if !ok {
			return
		}
		ident, ok2 := sel.X.(*ast.Ident)
		if !ok2 || ident.Name != alias {
			return
		}
		if sel.Sel.Name == "EventSpec" {
			pos := fset.Position(node.Pos())
			violations = append(violations, fmt.Sprintf(
				"%s:%d: manual %s.EventSpec() call — must be in generated/contracts/**/*_gen.go only",
				rel, pos.Line, alias,
			))
		}
	})
	return violations
}
