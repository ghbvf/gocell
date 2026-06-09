// INVARIANT: NO-DELETED-AUTH-SYMBOLS-01
package archtest

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestNO_DELETED_AUTH_SYMBOLS_01 enforces that no production or test code
// references the three deleted runtime/auth symbols. The scan runs typed
// over the seven production roots, paired with FlatNonDefaultTags() to cover
// build-tagged variants (e.g. //go:build integration test helpers).
func TestNO_DELETED_AUTH_SYMBOLS_01(t *testing.T) {
	t.Parallel()
	Report(t, ruleNoDeletedAuthSymbols01,
		CheckNoDeletedAuthSymbols01(t, ConfigForExternalCell{BuildTags: FlatNonDefaultTags()}))
}

// TestNO_DELETED_AUTH_SYMBOLS_01_FixtureCatchesAllForms exercises every
// import form the scanner must catch (default alias, custom alias,
// dot-imported func, same-package self-reference, same-package
// re-declaration) and the false-positive form it must reject (same-name
// decoy from a different package). The fixture-local auth import path
// replaces the runtime/auth path so the same scanner code path runs
// unchanged.
//
// Expected hit breakdown (drift would break exact-count assertion):
//
//   - auth/auth.go                 → 4 hits (3 re-decls via (C) info.Defs:
//     RoleInternalAdmin const + ServiceNameInternal const + BuiltinServiceRoles
//     func; + 1 self-use via (B): RoleInternalAdmin inside BuiltinServiceRoles
//     body). Closes BS-同包 — without (C) re-decl + self-use detection, the
//     fixture's auth package would silently bypass the rule.
//   - caller_default_alias.go      → 3 hits (2 consts + 1 func, default alias)
//   - caller_custom_alias.go       → 3 hits (2 consts + 1 func, custom alias)
//   - caller_dot_import.go         → 3 hits (2 consts + 1 func, dot-imported;
//     covered by (B) info.Uses type switch — Const/Var/Func/TypeName)
//   - caller_negative_other_pkg.go → 0 hits (same names, different package)
//   - caller_reflect_bs1.go        → 0 hits (BS-1 fixture; main scan ignores
//     string literals; verified by sibling BS-1 fixture test)
//   - doc.go                       → 0 hits (package documentation only)
//
// Per ai-robust.md §"Hard 范本": the fixture is a real Go package loaded via
// packages.Load with the archtest_fixture build tag. Bypassing this test
// requires modifying real source code.
func TestNO_DELETED_AUTH_SYMBOLS_01_FixtureCatchesAllForms(t *testing.T) {
	t.Parallel()

	diags := Run(t, Fixture(
		FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/nodeletedauthsymbolsfixture/..."},
	),
		func(p *Pass) []Diagnostic {
			return scanDeletedAuthSymbolsAgainst(p, fixtureAuthImportPath)
		})

	hitsByFile := map[string]int{}
	for _, d := range diags {
		hitsByFile[d.Rel]++
		t.Logf("fixture hit: %s:%d %s", d.Rel, d.Line, d.Message)
	}

	require.Len(t, diags, 13,
		"fixture must yield exactly 13 hits "+
			"(4 auth/auth.go re-decl+self + 3 default + 3 custom-alias + "+
			"3 dot-import + 0 negative + 0 reflect-bs1 + 0 doc); "+
			"any change in the fixture must update the expected count")

	// Per-file breakdown so a re-shuffled fixture cannot silently keep the
	// total constant while losing a form. doc.go and caller_reflect_bs1.go
	// assertions (0 hits each) catch silent drift if someone adds a banned
	// reference to those files. auth/auth.go assertion guards the BS-同包
	// closure (re-decl + self-use must both fire).
	type expect struct {
		suffix string
		want   int
	}
	expectations := []expect{
		{"auth/auth.go", 4},
		{"caller_default_alias.go", 3},
		{"caller_custom_alias.go", 3},
		{"caller_dot_import.go", 3},
		{"caller_negative_other_pkg.go", 0},
		{"caller_reflect_bs1.go", 0},
		{"doc.go", 0},
	}
	for _, e := range expectations {
		got := 0
		for rel, n := range hitsByFile {
			if strings.HasSuffix(rel, e.suffix) {
				got += n
			}
		}
		assert.Equal(t, e.want, got,
			"fixture %s must yield exactly %d hits; got %d (see logged diagnostics for actual)",
			e.suffix, e.want, got)
	}
}

// TestNO_DELETED_AUTH_SYMBOLS_01_BS1_NoReflectAccess implements the BS-1
// reverse self-check: no production code calls a reflect.* function with a
// string argument that contains one of the banned symbol names. This guards
// the obvious reflective-bypass pattern without requiring full reflect type
// tracing. Companion test
// TestNO_DELETED_AUTH_SYMBOLS_01_BS1_FixtureCatchesReflect verifies the
// scanner logic actually fires on a synthetic call site.
func TestNO_DELETED_AUTH_SYMBOLS_01_BS1_NoReflectAccess(t *testing.T) {
	t.Parallel()

	diags := Run(t, WorkspaceTyped(
		TypedOpts{Tests: true, Tags: FlatNonDefaultTags()},
		productionScanPatterns,
	),

		scanDeletedAuthSymbolsReflectBypass)

	assert.Empty(t, diags,
		"NO-DELETED-AUTH-SYMBOLS-01 BS-1: reflect call site references a banned "+
			"symbol name as a string literal; production code must not bypass the typed "+
			"funnel via reflection — remove the reflective lookup and call the replacement "+
			"API (auth.RequireCallerCell / auth.TestServiceContext) directly; "+
			"see PR #362 SVCTOKEN-CALLER-IDENTITY")
}

// TestNO_DELETED_AUTH_SYMBOLS_01_BS1_FixtureCatchesReflect pins the BS-1
// scanner's positive-detection contract: a synthetic reflect.<X>(...) call
// site whose string-literal argument names a banned symbol must produce
// exactly one diagnostic. Without this gate, a regression that disables the
// BS-1 scanner's string-match branch would leave the "no reflect access"
// assertion silently green forever (production scan stays at 0 hits regardless).
func TestNO_DELETED_AUTH_SYMBOLS_01_BS1_FixtureCatchesReflect(t *testing.T) {
	t.Parallel()

	diags := Run(t, Fixture(
		FixtureOpts{Tests: false},
		[]string{"./tools/archtest/internal/nodeletedauthsymbolsfixture/..."},
	),

		scanDeletedAuthSymbolsReflectBypass)

	require.Len(t, diags, 2,
		"BS-1 fixture must yield exactly 2 hits (FieldByName + MethodByName chained off reflect.ValueOf "+
			"in caller_reflect_bs1.go); got %d", len(diags))
	for _, d := range diags {
		assert.Contains(t, d.Rel, "caller_reflect_bs1.go",
			"BS-1 fixture hit must originate in caller_reflect_bs1.go")
	}
	var sawField, sawMethod bool
	for _, d := range diags {
		if strings.Contains(d.Message, reflectFieldByName) {
			sawField = true
		}
		if strings.Contains(d.Message, reflectMethodByName) {
			sawMethod = true
		}
	}
	assert.True(t, sawField, "BS-1 fixture must produce a FieldByName diagnostic")
	assert.True(t, sawMethod, "BS-1 fixture must produce a MethodByName diagnostic")
}

// scanDeletedAuthSymbolsReflectBypass delegates to the shared
// scanReflectStringArgCalls (REFLECT-STRING-ARG-SCANNER-01) which
// type-checks the receiver to reflect.Value and folds constant string
// arguments. Covers both call forms (method-value `v.MethodByName(name)` +
// method-expression `reflect.Value.MethodByName(v, name)`) and both methods
// (FieldByName for value reads, MethodByName for method lookup), so chained
// shapes like `reflect.ValueOf(x).MethodByName("BuiltinServiceRoles")` are
// caught — closing what would otherwise be the BS-1a chained-reflect
// residual.
func scanDeletedAuthSymbolsReflectBypass(p *Pass) []Diagnostic {
	if p.TypesInfo == nil {
		return nil
	}
	banned := func(n string) bool { return deletedAuthSymbols[n] }
	var diags []Diagnostic
	for _, file := range p.Files {
		rel := p.Rel(file)
		for _, method := range []string{reflectFieldByName, reflectMethodByName} {
			for _, hit := range scanReflectStringArgCalls(p, file, method, banned) {
				diags = append(diags, Diagnostic{
					Rel:  rel,
					Line: hit.Line,
					Message: fmt.Sprintf(
						"NO-DELETED-AUTH-SYMBOLS-01 BS-1: reflect.Value.%s called with %q — banned symbol; "+
							"replace with auth.RequireCallerCell (authz) or auth.TestServiceContext (test principals); "+
							"see PR #362 SVCTOKEN-CALLER-IDENTITY",
						method, hit.Name,
					),
				})
			}
		}
	}
	return diags
}
