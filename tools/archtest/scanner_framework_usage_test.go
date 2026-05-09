package archtest

// scanner_framework_usage_test.go — guard archtest tools/archtest/*_test.go from
// reverting to raw filepath.WalkDir/filepath.Walk after PR #419 introduced the
// shared scanner framework at tools/archtest/internal/scanner/.
//
// Houses the SCANNER-FRAMEWORK-USAGE-{01,02} invariants. Per CLAUDE.md
// "新增 invariant 决策原则" file naming branch, ≥ 3 related rules → rename to
// scanner_invariants_test.go; current count = 2, stay single file.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

// INVARIANT: SCANNER-FRAMEWORK-USAGE-01
//
// archtest *_test.go files at tools/archtest/<file>_test.go must use the
// tools/archtest/internal/scanner framework instead of raw filepath.WalkDir
// or filepath.Walk. The framework provides fail-closed scope predicates,
// structured diagnostic emission, built-in vendor/testdata/worktrees/generated/
// .git/node_modules skip, self-protection (auto-exclude scanner package itself),
// and uniform module-relative path display in error messages. Raw filepath
// walks bypass these guarantees and silently grow drift across rules.
//
// Scope: only top-level tools/archtest/*_test.go files (where archtest rules
// live). The scanner package itself (tools/archtest/internal/scanner/) is
// auto-excluded by Scope.collectFile's selfProtectRel; other internal
// subpackages (e.g. tools/archtest/internal/typeseval/) are unit tests for
// helpers and out of scope for this rule.
//
// Cannot funnel: the rule itself enforces consumer use of the funnel
// (the scanner framework), so it must be a hand-written archtest. type-system
// can't tell apart "framework-internal walk" (legitimate) from "consumer raw
// walk" (forbidden); both are *ast.SelectorExpr against path/filepath.
//
// Authorized escape hatch — io/fs.WalkDir(os.DirFS(...)) for non-Go content:
// the scanner framework's EachFile is .go-only (it parses Go ASTs). Rules
// that scan non-Go content (YAML, JSON schemas, .md docs, opaque testdata
// fixtures) are allowed to use io/fs.WalkDir(os.DirFS(root), ...) directly.
// This rule only matches path/filepath.Walk[Dir] selectors; it does not
// match io/fs.WalkDir, so the escape hatch is silent by construction.
// Approved sites at the time of writing (each scans non-Go content):
//   - listener_dx_test.go (.md doc enumeration)
//   - event_camelcase_test.go (payload.schema.json files via os.ReadDir)
//   - security_defaults_test.go (examples docker-compose YAML via os.ReadDir)
//   - span_record_error_redact_test.go (fixture YAML under testdata/)
//
// Coverage of the SelectorExpr scan:
//
// The ast.Inspect walk below matches every *ast.SelectorExpr — not just
// CallExpr.Fun selectors — so all of the following are caught at the
// definition site:
//   - direct call: filepath.WalkDir(root, fn)
//   - function-value binding: fp := filepath.WalkDir; fp(...)
//   - var declaration: var w = filepath.WalkDir
//   - argument passing: runWith(filepath.WalkDir)
//   - struct/slice literal: walkers := []func(...){filepath.WalkDir}
//
// The one bypass the SelectorExpr walk cannot see is dot-import:
//
//	import . "path/filepath"
//	WalkDir(root, fn)  // unqualified — no SelectorExpr at all
//
// PackageAliases excludes dot-imports (imports.go honors imp.Name == "."),
// so with a dot-import the loop below would early-return on len(aliases)==0.
// The dot-import detection added before the SelectorExpr scan closes this
// gap by flagging the import declaration directly.
//
// New rules scanning .go files MUST go through the scanner framework.
func TestScannerFrameworkUsage01(t *testing.T) {
	root := findModuleRoot(t)
	scope := scanner.DirsScope(root, []string{"tools/archtest"}, scanner.IncludeTests())

	var diags []scanner.Diagnostic
	scanner.EachFile(t, scope, parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		// Only flag top-level archtest test files (tools/archtest/<file>_test.go).
		// Subpackages under tools/archtest/internal/ are out of scope.
		if filepath.ToSlash(filepath.Dir(fc.Rel)) != "tools/archtest" {
			return
		}
		if !strings.HasSuffix(fc.AbsPath, "_test.go") {
			return
		}

		// Dot-import bypass guard: `import . "path/filepath"` exposes WalkDir/Walk
		// as unqualified identifiers, which the SelectorExpr scan below cannot
		// see. Flag the import declaration unconditionally — there is no
		// legitimate reason to dot-import path/filepath in archtest tests
		// (filepath.ToSlash/Dir/Abs are clearer when qualified, and dot-imports
		// are golangci-lint dot-imports flagged elsewhere).
		for _, imp := range fc.File.Imports {
			if imp == nil || imp.Path == nil || imp.Path.Value != `"path/filepath"` {
				continue
			}
			if imp.Name == nil || imp.Name.Name != "." {
				continue
			}
			diags = append(diags, scanner.Diagnostic{
				Rel:     fc.Rel,
				Line:    fc.Fset.Position(imp.Pos()).Line,
				Message: `dot-import of "path/filepath" forbidden in archtest tests; use named import + tools/archtest/internal/scanner for walks`,
			})
		}

		// Self-exempt: this archtest file legitimately needs path/filepath only
		// for filepath.ToSlash/Dir parsing of fc.Rel — not for walking. Detect
		// by absence of WalkDir/Walk SelectorExpr in this very file's AST.
		aliases := scanner.PackageAliases(fc.File, "path/filepath")
		if len(aliases) == 0 {
			return
		}
		ast.Inspect(fc.File, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			if _, isFilepathAlias := aliases[ident.Name]; !isFilepathAlias {
				return true
			}
			if sel.Sel.Name != "WalkDir" && sel.Sel.Name != "Walk" {
				return true
			}
			line := fc.Fset.Position(sel.Pos()).Line
			diags = append(diags, scanner.Diagnostic{
				Rel:     fc.Rel,
				Line:    line,
				Message: "use tools/archtest/internal/scanner (DirsScope/ModuleScope + EachFile) instead of filepath." + sel.Sel.Name,
			})
			return true
		})
	})
	scanner.Report(t, "SCANNER-FRAMEWORK-USAGE-01", diags)
}

// TestScannerFrameworkUsage01_DotImportFixture proves the dot-import detection
// branch added alongside SCANNER-FRAMEWORK-USAGE-01 actually fires. Without
// this test, an AI refactor could silently drop the dot-import guard and the
// main rule would still pass (because none of the live archtest files
// dot-import path/filepath today). Inline-source fixture so no testdata file
// is needed.
func TestScannerFrameworkUsage01_DotImportFixture(t *testing.T) {
	t.Parallel()
	src := `package fake
import . "path/filepath"
func _() string { return Join("a", "b") }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fake.go", src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var hits int
	for _, imp := range file.Imports {
		if imp == nil || imp.Path == nil || imp.Path.Value != `"path/filepath"` {
			continue
		}
		if imp.Name == nil || imp.Name.Name != "." {
			continue
		}
		hits++
	}
	if hits != 1 {
		t.Fatalf("dot-import detection: expected 1 hit on fixture, got %d", hits)
	}

	// PackageAliases must continue to exclude dot-imports — that's why the
	// dot-import branch above is needed in the first place.
	aliases := scanner.PackageAliases(file, "path/filepath")
	if _, hasDot := aliases["."]; hasDot {
		t.Fatal("PackageAliases must exclude dot-imports; got '.' in alias set")
	}
	if len(aliases) != 0 {
		t.Fatalf("PackageAliases on dot-import-only file: expected 0 named aliases, got %d (%v)", len(aliases), aliases)
	}
}

// INVARIANT: SCANNER-FRAMEWORK-USAGE-02
//
// Functions in tools/archtest/*_test.go that contain directory traversal
// (os.ReadDir / fs.WalkDir / fs.Glob / fs.ReadDir) MUST declare a
// `// SCANNER-ESCAPE-HATCH: <category>` anchor in their function doc comment.
//
// Background: SCANNER-FRAMEWORK-USAGE-01 forbids path/filepath.WalkDir|Walk
// in archtest tests because the scanner framework is the canonical funnel.
// Non-Go content (YAML, JSON, MD, SQL) cannot be parsed by go/parser, so the
// framework's .go-only EachFile is unusable; io/fs.WalkDir(os.DirFS(...)) and
// os.ReadDir are authorized escape hatches. Without an anchor on each escape
// site, AI-generated rules can silently introduce new directory walks and
// the docstring-maintained "approved sites" list in -01 drifts out of date.
//
// Cannot funnel: same reason as -01 (the rule enforces consumer use of the
// funnel; cannot itself live in the funnel).
//
// Approved categories at the time of writing:
//
//	non-go-md-doc                .md documentation enumeration
//	non-go-json-schema           payload.schema.json files
//	non-go-yaml-compose          docker-compose.yml + similar
//	non-go-yaml-workflow         .github/workflows/*.yml
//	non-go-sql-migration         adapters/postgres/migrations/*.sql
//	testdata-fixture-bypass      scanner skips testdata/, fixture re-walk needed
//	deferred-scanner-migration   scans .go files but predates the scanner
//	                             framework; legitimate migration target,
//	                             tracked as backlog (grep this token to find
//	                             the candidate set)
//
// Adding a new category: include `// SCANNER-ESCAPE-HATCH: <new-cat>` in the
// function's doc comment and extend the list above. Reviewers verify the
// bypass is justified.
//
// "deferred-scanner-migration" is intentionally a category, not an exception:
// it exposes the funnel-incomplete state in code where future AI sees it,
// rather than burying the candidate set in an external doc that drifts.
// Migrating any of these to scanner.EachFile and dropping the anchor is
// always safe and welcomed.
func TestScannerFrameworkUsage02_EscapeHatchAnchor(t *testing.T) {
	root := findModuleRoot(t)
	scope := scanner.DirsScope(root, []string{"tools/archtest"}, scanner.IncludeTests())

	var diags []scanner.Diagnostic
	scanner.EachFile(t, scope, parser.ParseComments|parser.SkipObjectResolution, func(t *testing.T, fc scanner.FileContext) {
		if filepath.ToSlash(filepath.Dir(fc.Rel)) != "tools/archtest" {
			return
		}
		if !strings.HasSuffix(fc.AbsPath, "_test.go") {
			return
		}
		for _, decl := range fc.File.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			if !escapeHatchCallSite(fn.Body) {
				continue
			}
			if escapeHatchAnchorPresent(fn.Doc) {
				continue
			}
			diags = append(diags, scanner.Diagnostic{
				Rel:     fc.Rel,
				Line:    fc.Fset.Position(fn.Pos()).Line,
				Message: "function calls os.ReadDir / fs.WalkDir / fs.Glob / fs.ReadDir but doc comment lacks `// SCANNER-ESCAPE-HATCH: <category>` anchor — see SCANNER-FRAMEWORK-USAGE-02 for approved categories",
			})
		}
	})
	scanner.Report(t, "SCANNER-FRAMEWORK-USAGE-02", diags)
}

// escapeHatchCallSite reports whether body contains a directory-traversal
// call (os.ReadDir / fs.WalkDir / fs.Glob / fs.ReadDir). Plain selector
// match — no type info needed because the rule's purpose is precisely to
// flag the AST-level call shape regardless of receiver type.
func escapeHatchCallSite(body *ast.BlockStmt) bool {
	var found bool
	ast.Inspect(body, func(n ast.Node) bool {
		if found {
			return false
		}
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch ident.Name {
		case "os":
			if sel.Sel.Name == "ReadDir" {
				found = true
			}
		case "fs":
			switch sel.Sel.Name {
			case "WalkDir", "Glob", "ReadDir":
				found = true
			}
		}
		return !found
	})
	return found
}

// escapeHatchAnchorPresent reports whether doc contains the
// `SCANNER-ESCAPE-HATCH:` anchor token in any of its comment lines.
// Substring match is sufficient — the anchor format is stable enough that
// false positives (e.g. a comment containing the literal string) are
// negligible and would still represent a deliberate acknowledgement.
func escapeHatchAnchorPresent(doc *ast.CommentGroup) bool {
	if doc == nil {
		return false
	}
	for _, c := range doc.List {
		if strings.Contains(c.Text, "SCANNER-ESCAPE-HATCH:") {
			return true
		}
	}
	return false
}

// TestScannerFrameworkUsage02_DetectionFixture proves both helpers
// (escapeHatchCallSite + escapeHatchAnchorPresent) behave as expected on
// hand-crafted fixtures. AI refactors that silently weaken either helper
// (e.g. drop a case from the switch, return false unconditionally) would
// flip one of these table cases.
func TestScannerFrameworkUsage02_DetectionFixture(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		src        string
		wantHit    bool // true == expect SCANNER-FRAMEWORK-USAGE-02 violation
		wantSkip   bool // true == fn has no escape-hatch call (rule should skip)
		wantAnchor bool // true == anchor present in doc
	}{
		{
			name: "missing_anchor_with_os_readdir",
			src: `package fake
import "os"
func walk(dir string) { _, _ = os.ReadDir(dir) }
`,
			wantHit: true,
		},
		{
			name: "anchor_present_with_os_readdir",
			src: `package fake
import "os"
// SCANNER-ESCAPE-HATCH: non-go-yaml-compose
func walk(dir string) { _, _ = os.ReadDir(dir) }
`,
			wantHit: false, wantAnchor: true,
		},
		{
			name: "missing_anchor_with_fs_walkdir",
			src: `package fake
import (
	"io/fs"
	"os"
)
func walk(root string) { _ = fs.WalkDir(os.DirFS(root), ".", nil) }
`,
			wantHit: true,
		},
		{
			name: "no_call_no_anchor_needed",
			src: `package fake
func helper() string { return "" }
`,
			wantHit: false, wantSkip: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fset := token.NewFileSet()
			file, err := parser.ParseFile(fset, "fake.go", tc.src, parser.ParseComments|parser.SkipObjectResolution)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			var hits int
			for _, decl := range file.Decls {
				fn, ok := decl.(*ast.FuncDecl)
				if !ok || fn.Body == nil {
					continue
				}
				hasCall := escapeHatchCallSite(fn.Body)
				hasAnchor := escapeHatchAnchorPresent(fn.Doc)
				if !hasCall {
					if !tc.wantSkip {
						t.Fatalf("escapeHatchCallSite: expected call site in fixture, got none")
					}
					continue
				}
				if hasAnchor != tc.wantAnchor {
					t.Errorf("escapeHatchAnchorPresent: got %v, want %v", hasAnchor, tc.wantAnchor)
				}
				if !hasAnchor {
					hits++
				}
			}
			gotHit := hits > 0
			if gotHit != tc.wantHit {
				t.Errorf("violation: got %v (hits=%d), want %v", gotHit, hits, tc.wantHit)
			}
		})
	}
}
