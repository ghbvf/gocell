// INVARIANT: BCRYPT-COST-FUNNEL-01
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
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
)

const (
	bcryptModulePath     = "golang.org/x/crypto/bcrypt"
	credentialModulePath = "github.com/ghbvf/gocell/cells/accesscore/internal/credential"

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

// TestBCRYPT_COST_FUNNEL_01_A1_SingleHashOrigin fails if bcrypt.GenerateFromPassword
// appears in any production (non-test) file other than credential/hasher.go.
// Replaces the compile-time guarantee the deleted domain.BcryptCost const gave.
func TestBCRYPT_COST_FUNNEL_01_A1_SingleHashOrigin(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	// ModuleScope without IncludeTests → production files only. Test files may
	// hash fixtures directly (seed a known password into a store); that is not
	// production hashing and is out of A1's scope.
	files, err := scanner.ModuleScope(root).Files()
	require.NoError(t, err)

	var findings []string
	for _, path := range files {
		rel := relSlash(root, path)
		if rel == bcryptHasherRel {
			continue // the sanctioned holder
		}
		line, ok, perr := firstQualifiedSelectorLine(path, bcryptModulePath, "bcrypt", "GenerateFromPassword")
		require.NoError(t, perr)
		if ok {
			findings = append(findings, fmt.Sprintf(
				"%s:%d: bcrypt.GenerateFromPassword outside credential.Hasher", rel, line,
			))
		}
	}
	assert.Empty(t, findings,
		"all password hashing must route through cells/accesscore/internal/credential.Hasher "+
			"(NewProductionHasher / NewTestHasher); bcrypt.GenerateFromPassword may appear only in "+bcryptHasherRel)
}

// TestBCRYPT_COST_FUNNEL_01_A2_TestHasherCallerAllowlist fails if
// credential.NewTestHasher is called from any non-test, non-allowlisted file.
// The production path has only NewProductionHasher (no cost knob), so there is
// no way to express a weaker-cost production hasher.
func TestBCRYPT_COST_FUNNEL_01_A2_TestHasherCallerAllowlist(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	files, err := scanner.ModuleScope(root, scanner.IncludeTests()).Files()
	require.NoError(t, err)

	var findings []string
	for _, path := range files {
		rel := relSlash(root, path)
		if newTestHasherCallerAllowed(rel) {
			continue
		}
		line, ok, perr := firstQualifiedSelectorLine(path, credentialModulePath, "credential", "NewTestHasher")
		require.NoError(t, perr)
		if ok {
			findings = append(findings, fmt.Sprintf(
				"%s:%d: credential.NewTestHasher called outside test code", rel, line,
			))
		}
	}
	assert.Empty(t, findings,
		"credential.NewTestHasher is the low-cost test door; it may be called only from *_test.go "+
			"or sanctioned test-support packages. Production wires credential.NewProductionHasher().")
}

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
	alias, ok := moduleImportAlias(file, modulePath, defaultName)
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

// moduleImportAlias returns the local import name bound to modulePath in file,
// or ("",false) if not imported or dot/blank-imported.
func moduleImportAlias(file *ast.File, modulePath, defaultName string) (string, bool) {
	for _, imp := range file.Imports {
		if archStringLiteralValue(imp.Path) != modulePath {
			continue
		}
		if name := importSelectorName(imp, defaultName); name != "" {
			return name, true
		}
	}
	return "", false
}

// TestBCRYPT_COST_FUNNEL_01_A1_RedFixture asserts the A1 detector fires on a
// known-positive: internal/bcryptcostredfixture/fixture.go deliberately calls
// bcrypt.GenerateFromPassword. The fixture lives under tools/archtest/internal,
// which the scanner excludes from the production scan, so it does not pollute
// A1's real findings. Without this, a broken detector would silently pass.
func TestBCRYPT_COST_FUNNEL_01_A1_RedFixture(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	fixturePath := filepath.Join(root, "tools", "archtest", "internal", "bcryptcostredfixture", "fixture.go")
	line, ok, err := firstQualifiedSelectorLine(fixturePath, bcryptModulePath, "bcrypt", "GenerateFromPassword")
	require.NoError(t, err, "parse A1 RED fixture")
	if !ok {
		t.Error("BCRYPT-COST-FUNNEL-01 A1 RED fixture: detector found no bcrypt.GenerateFromPassword in fixture.go; " +
			"firstQualifiedSelectorLine may be broken")
		return
	}
	t.Logf("A1 RED fixture hit at fixture.go:%d", line)
}

// TestBCRYPT_COST_FUNNEL_01_A2_RedFixture asserts the A2 detector fires on a
// known-positive: credential.NewTestHasher called from a (would-be) non-test
// file. Unlike A1's committed fixture (which imports the public bcrypt package
// and compiles), an A2 fixture would have to import cells/accesscore/internal/credential
// — illegal from tools/archtest under Go's internal rule, and committing a
// //go:build ignore file introduces an "ignore" build tag that the repo's
// build-tag governance rejects. So the source is written to a temp file and
// parsed (never compiled): parser.ParseFile reads it, the toolchain never does.
func TestBCRYPT_COST_FUNNEL_01_A2_RedFixture(t *testing.T) {
	t.Parallel()
	src := "package redfixture\n\n" +
		"import \"" + credentialModulePath + "\"\n\n" +
		"var _ = credential.NewTestHasher(4)\n"
	path := filepath.Join(t.TempDir(), "a2_red.go")
	require.NoError(t, os.WriteFile(path, []byte(src), 0o600))

	line, ok, err := firstQualifiedSelectorLine(path, credentialModulePath, "credential", "NewTestHasher")
	require.NoError(t, err, "parse A2 RED fixture")
	if !ok {
		t.Error("BCRYPT-COST-FUNNEL-01 A2 RED fixture: detector found no credential.NewTestHasher; " +
			"firstQualifiedSelectorLine may be broken")
		return
	}
	t.Logf("A2 RED fixture hit at line %d", line)
}

// TestBCRYPT_COST_FUNNEL_01_NoDotImportBlindSpot closes the dot-import blind
// spot for BOTH funnel symbols: a dot-import would make GenerateFromPassword /
// NewTestHasher a bare ident the SelectorExpr scan misses. Asserts no file
// dot-imports bcrypt or the credential package (also globally banned by revive).
func TestBCRYPT_COST_FUNNEL_01_NoDotImportBlindSpot(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	files, err := scanner.ModuleScope(root, scanner.IncludeTests()).Files()
	require.NoError(t, err)

	var findings []string
	for _, path := range files {
		rel := relSlash(root, path)
		for _, mod := range []string{bcryptModulePath, credentialModulePath} {
			dot, perr := fileDotImportsModule(path, mod)
			require.NoError(t, perr)
			if dot {
				findings = append(findings, fmt.Sprintf("%s: dot-import of %s evades BCRYPT-COST-FUNNEL-01", rel, mod))
			}
		}
	}
	assert.Empty(t, findings,
		"dot-importing bcrypt or the credential package would make the funnel symbols bare idents and evade the scan; forbidden")
}

func fileDotImportsModule(path, modulePath string) (bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return false, err
	}
	for _, imp := range file.Imports {
		if archStringLiteralValue(imp.Path) != modulePath {
			continue
		}
		if imp.Name != nil && imp.Name.Name == "." {
			return true, nil
		}
	}
	return false, nil
}
