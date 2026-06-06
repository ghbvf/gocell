package archtest

// bcrypt_cost_funnel.go — importable BCRYPT-COST-FUNNEL-01 rule logic
// (#1632 M3).
//
// This is the non-test home of the scanner logic so it can be compiled and run
// by an external Cell repository (Go never compiles a dependency's _test.go,
// so rule logic external repos must run cannot live in a _test.go file).
// GoCell's own Test* functions in bcrypt_cost_funnel_test.go call the same
// Check* — single source, no parallel rule body.
//
// # BCRYPT-COST-FUNNEL-01
//
// All accesscore password hashing routes through the single
// cells/accesscore/internal/credential.Hasher, whose bcrypt cost is chosen by
// which constructor minted it:
//
//   - credential.NewProductionHasher()  — hardwired credential.ProductionCost
//     (=12). No cost parameter exists, so the production path is structurally
//     incapable of expressing a weaker cost.
//   - credential.NewTestHasher(cost)    — the low-cost test door; cuts the
//     ~1.5s/hash (bcrypt cost 12 under -race) that made cells/accesscore/slices/setup
//     an 82s race-unit outlier and l2atomicity seedAdmin a per-test tax.
//
// Two rules close the funnel (per ai-robust.md §Hard 技术族目录):
//
//   - A1 "single sanctioned holder": bcrypt.GenerateFromPassword may appear
//     only in credential/hasher.go. This is what makes the const-12 guarantee
//     that the deleted domain.BcryptCost used to give survive injection — no
//     other production file can hash at all, so none can pick a cost.
//   - A2 "typed function choice": credential.NewTestHasher (the only cost-bearing
//     door) may be called only from sanctioned test locations. Selecting the
//     wrong semantics is selecting the wrong function name, not passing a wrong
//     int — there is no "looks-right-but-isn't" gray zone of a literal that
//     happens to equal a low cost.
//
// # AI-robust grade
//
// Downstream Hard (A1 callee-location + A2 caller-allowlist, both archtest-locked
// by callsite identity). Upstream Medium: Go has no friend-package, so the
// compiler cannot stop a NEW direct bcrypt.GenerateFromPassword call added
// inside the credential package itself from bypassing Hasher.Hash; archtest
// catches it in CI but the type system does not reject it. Upstream Hard-ization
// is tracked by gh issue #901 (see §Funnel 双向锁评级).
//
// # Why pure-AST (not the typed façade)
//
// Both rules resolve a callsite by import-path + alias + selector name — no
// receiver-type / interface-implementation / const-evaluation is needed. Per
// ai-robust.md §载体决策原则, that is the "纯 AST 模式" route. Mirrors the sibling
// PG-TESTCONTAINER-FUNNEL-01.
//
// # Blind-spot inventory (per ai-robust.md §"工具选定后强制盲区自检")
//
//   - Function-value reference `gen := bcrypt.GenerateFromPassword; gen(...)`:
//     COVERED — the scan walks every <alias>.<sel> SelectorExpr (assignment RHS,
//     arg pass, or CallExpr.Fun alike), not just call targets.
//   - Dot-import `import . ".../bcrypt"; GenerateFromPassword(...)` (or the same
//     for credential): the symbol becomes a bare *ast.Ident the SelectorExpr
//     scan misses. The authoritative guard is the reverse self-test
//     TestBCRYPT_COST_FUNNEL_01_NoDotImportBlindSpot, which asserts no file
//     dot-imports either module (revive's dot-imports lint is a supplementary,
//     not relied-upon, layer).
//   - Reflection-based construction: out of scope, treated as theoretical.
//
// Not registered in StandardCellRules: allowlist is gocell-hardcoded with no
// ConfigForExternalCell consumer-extension → would false-red an external cell's
// own auth code; kept importable & module-path-agnostic but vacuous-green externally.

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const (
	bcryptModulePath     = "golang.org/x/crypto/bcrypt"
	credentialModulePath = PlatformModulePath + "/cells/accesscore/internal/credential"

	// bcryptHasherRel is the single sanctioned file allowed to call
	// bcrypt.GenerateFromPassword (A1). Module-relative, slash form.
	bcryptHasherRel = "cells/accesscore/internal/credential/hasher.go"
)

// newTestHasherCallerAllowlist holds the module-relative locations permitted to
// call credential.NewTestHasher (A2). *_test.go is handled separately by suffix;
// this slice adds non-_test.go test-support packages (importable only by tests).
//
// accesscoretest is the sanctioned bridge letting external test packages (e.g.
// tests/integration harnesses, which cannot import the internal credential
// package under Go's internal rule) obtain a low-cost hasher via
// accesscoretest.MinCostPasswordHasherOption(). That accesscoretest is imported ONLY
// by tests is a convention, not a compiler-enforced barrier — this is the
// upstream-Medium edge of the funnel (A1/A2 downstream are Hard). The
// Hard-ization path is a generic "test-support packages imported only by
// *_test.go" guard.
var newTestHasherCallerAllowlist = []string{
	"cells/accesscore/accesscoretest/", // test-support builders, imported only by *_test.go
}

// newTestHasherCallerAllowed reports whether the module-relative path rel is
// permitted to call credential.NewTestHasher (A2).
func newTestHasherCallerAllowed(rel string) bool {
	if strings.HasSuffix(rel, "_test.go") {
		return true
	}
	for _, prefix := range newTestHasherCallerAllowlist {
		if strings.HasPrefix(rel, prefix) {
			return true
		}
	}
	return false
}

// firstQualifiedSelectorLine returns the line of the first <alias>.<selName>
// SelectorExpr in path, where <alias> is the local import name bound to
// modulePath (handles named/aliased imports). A dot-import returns
// ("",false) → not matched here; that blind spot is closed by the dedicated
// reverse self-tests. Walking SelectorExpr (not just CallExpr.Fun) also catches
// function-value references like `gen := pkg.Symbol`.
func firstQualifiedSelectorLine(path, modulePath, defaultName, selName string) (int, bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return 0, false, err
	}
	alias, ok := bcryptModuleImportAlias(file, modulePath, defaultName)
	if !ok {
		return 0, false, nil
	}
	var pos token.Pos
	scanner.EachInSubtree[ast.SelectorExpr](file, func(sel *ast.SelectorExpr) {
		if pos.IsValid() {
			return
		}
		id, isIdent := sel.X.(*ast.Ident)
		if isIdent && id.Name == alias && sel.Sel.Name == selName {
			pos = sel.Pos()
		}
	})
	if pos.IsValid() {
		return fset.Position(pos).Line, true, nil
	}
	return 0, false, nil
}

// bcryptModuleImportAlias returns the local import name bound to modulePath in
// file, or ("",false) if not imported or dot/blank-imported. Named with a
// bcrypt-scoped prefix to avoid collision with identically-named helpers in
// _test.go files (e.g. integration_guard_test.go).
func bcryptModuleImportAlias(file *ast.File, modulePath, defaultName string) (string, bool) {
	for _, imp := range file.Imports {
		if bcryptStringLiteralValue(imp.Path) != modulePath {
			continue
		}
		if name := bcryptImportSelectorName(imp, defaultName); name != "" {
			return name, true
		}
	}
	return "", false
}

// bcryptStringLiteralValue unquotes an AST BasicLit string expression and
// returns its value, or "" on failure. Named with a bcrypt-scoped prefix to
// avoid collision with identically-named helpers in _test.go files.
func bcryptStringLiteralValue(expr ast.Expr) string {
	lit, ok := expr.(*ast.BasicLit)
	if !ok {
		return ""
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return ""
	}
	return value
}

// bcryptImportSelectorName returns the local name for imp: the explicit alias
// if set (unless "." or "_"), or defaultName otherwise. Named with a
// bcrypt-scoped prefix to avoid collision with identically-named helpers in
// _test.go files.
func bcryptImportSelectorName(imp *ast.ImportSpec, defaultName string) string {
	if imp.Name == nil {
		return defaultName
	}
	switch imp.Name.Name {
	case ".", "_":
		return ""
	default:
		return imp.Name.Name
	}
}

// fileDotImportsModule reports whether path dot-imports modulePath.
func fileDotImportsModule(path, modulePath string) (bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return false, err
	}
	for _, imp := range file.Imports {
		if bcryptStringLiteralValue(imp.Path) != modulePath {
			continue
		}
		if imp.Name != nil && imp.Name.Name == "." {
			return true, nil
		}
	}
	return false, nil
}

// bcryptRelSlash returns the module-relative slash path for the given absolute
// path. Named with a bcrypt-scoped prefix to avoid collision with
// identically-named helpers in _test.go files (e.g. pgquery_boundary_test.go).
func bcryptRelSlash(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// CheckBcryptCostFunnel01 runs both A1 and A2 sub-rules of
// BCRYPT-COST-FUNNEL-01 over the running module and returns their combined
// diagnostics. It is the importable rule body and the single source for the
// rule: GoCell's TestBCRYPT_COST_FUNNEL_01 dogfoods it via Report(...), so the
// exact scan an external cell would import is the one GoCell enforces (no
// parallel rule body).
//
// Not registered in StandardCellRules: allowlist is gocell-hardcoded with no
// ConfigForExternalCell consumer-extension → would false-red an external cell's
// own auth code; kept importable & module-path-agnostic but vacuous-green
// externally.
func CheckBcryptCostFunnel01(t *testing.T, _ ConfigForExternalCell) []Diagnostic { //nolint:gocognit,lll // R2-approved: two-prong A1/A2 linear scan; same shape as PG-TESTCONTAINER-FUNNEL-01
	t.Helper()

	root := findModuleRoot(t)

	// A1: bcrypt.GenerateFromPassword must appear only in credential/hasher.go.
	filesA1, err := scanner.ModuleScope(root).Files()
	if err != nil {
		t.Fatalf("BCRYPT-COST-FUNNEL-01 A1: scanner.ModuleScope: %v", err)
	}
	var diags []Diagnostic
	for _, path := range filesA1 {
		rel := bcryptRelSlash(root, path)
		if rel == bcryptHasherRel {
			continue // the sanctioned holder
		}
		line, ok, perr := firstQualifiedSelectorLine(path, bcryptModulePath, "bcrypt", "GenerateFromPassword")
		if perr != nil {
			t.Fatalf("BCRYPT-COST-FUNNEL-01 A1: parse %s: %v", rel, perr)
		}
		if ok {
			diags = append(diags, Diagnostic{
				Rel:     rel,
				Line:    line,
				Message: "bcrypt.GenerateFromPassword outside credential.Hasher",
			})
		}
	}

	// A2: credential.NewTestHasher must be called only from test / allowlisted files.
	filesA2, err := scanner.ModuleScope(root, scanner.IncludeTests()).Files()
	if err != nil {
		t.Fatalf("BCRYPT-COST-FUNNEL-01 A2: scanner.ModuleScope: %v", err)
	}
	for _, path := range filesA2 {
		rel := bcryptRelSlash(root, path)
		if newTestHasherCallerAllowed(rel) {
			continue
		}
		line, ok, perr := firstQualifiedSelectorLine(path, credentialModulePath, "credential", "NewTestHasher")
		if perr != nil {
			t.Fatalf("BCRYPT-COST-FUNNEL-01 A2: parse %s: %v", rel, perr)
		}
		if ok {
			diags = append(diags, Diagnostic{
				Rel:     rel,
				Line:    line,
				Message: "credential.NewTestHasher called outside test code",
			})
		}
	}

	return diags
}
