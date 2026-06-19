package archtestrunner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// wrapRule wraps a func/decl body in a minimal package-archtest test file. The
// source only needs to parse (the runner AST-parses tools/archtest; it never
// type-checks it), so scope constructors are referenced as bare identifiers
// without importing the archtest package.
func wrapRule(body string) string {
	return "//go:build archtest\n\n// INVARIANT: X-01\n\npackage archtest\n\nimport \"testing\"\n\n" + body + "\n"
}

// domainOf builds the domain index over a fixture tools/archtest dir and
// returns the domain for one test func (zero value when absent).
func domainOf(t *testing.T, files map[string]string, fn string) fileDomain {
	t.Helper()
	root := makeFakeArchtestDir(t, files)
	idx, err := buildFileDomainIndex(root)
	require.NoError(t, err)
	return idx[fn]
}

// TestBuildFileDomainIndex_Extraction is the extractor table: synthetic
// archtest source → derived fileDomain.
func TestBuildFileDomainIndex_Extraction(t *testing.T) {
	cases := []struct {
		name string
		body string
		fn   string
		want fileDomain
	}{
		{
			name: "production scope",
			body: `func TestProd(t *testing.T) { Run(t, Production(TypedOpts{Tests: false}), nil) }`,
			fn:   "TestProd",
			want: fileDomain{scoped: true, productionGo: true},
		},
		{
			name: "typed literal prefix",
			body: `func TestRedis(t *testing.T) {
	Run(t, Typed(TypedOpts{}, []string{"./adapters/redis/..."}), nil)
}`,
			fn:   "TestRedis",
			want: fileDomain{scoped: true, prefixes: []string{"adapters/redis"}},
		},
		{
			name: "dirsscope literal via scope var",
			body: `func TestAsm(t *testing.T) {
	scope := DirsScope(root, []string{"framework/kernel/assembly"})
	Run(t, AST(scope), nil)
}`,
			fn:   "TestAsm",
			want: fileDomain{scoped: true, prefixes: []string{"framework/kernel/assembly"}},
		},
		{
			name: "scanner-qualified dirsscope literal",
			body: `func TestCg(t *testing.T) {
	Run(t, AST(scanner.DirsScope(root, []string{"tools/codegen/contractgen"})), nil)
}`,
			fn:   "TestCg",
			want: fileDomain{scoped: true, prefixes: []string{"tools/codegen/contractgen"}},
		},
		{
			name: "computed typed var is unknown",
			body: `func TestVar(t *testing.T) {
	pkgs := loadPkgs()
	Run(t, Typed(TypedOpts{}, pkgs), nil)
}`,
			fn:   "TestVar",
			want: fileDomain{}, // unknown
		},
		{
			name: "modulescope whole-module is unknown",
			body: `func TestMod(t *testing.T) { Run(t, AST(ModuleScope(root)), nil) }`,
			fn:   "TestMod",
			want: fileDomain{}, // unknown
		},
		{
			name: "glob mid-path is unknown",
			body: `func TestGlob(t *testing.T) { Run(t, Typed(TypedOpts{}, []string{"./foo/*/bar"}), nil) }`,
			fn:   "TestGlob",
			want: fileDomain{}, // unknown
		},
		{
			name: "no scope is unknown",
			body: `func TestNone(t *testing.T) { _ = 1 }`,
			fn:   "TestNone",
			want: fileDomain{}, // unknown
		},
		{
			name: "p.Typed() method does not collide with Production",
			body: `func TestColl(t *testing.T) {
	Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic {
		if !p.Typed() {
			return nil
		}
		return nil
	})
}`,
			fn:   "TestColl",
			want: fileDomain{scoped: true, productionGo: true},
		},
		{
			name: "fixture-only prefix",
			body: `func TestFix(t *testing.T) {
	Run(t, Fixture(FixtureOpts{}, []string{"./tools/archtest/internal/foofixture/..."}), nil)
}`,
			fn:   "TestFix",
			want: fileDomain{scoped: true, prefixes: []string{"tools/archtest/internal/foofixture"}},
		},
		{
			name: "multiple typed literals union",
			body: `func TestMulti(t *testing.T) {
	Run(t, Typed(TypedOpts{}, []string{"./adapters/redis/...", "./adapters/postgres/..."}), nil)
}`,
			fn:   "TestMulti",
			want: fileDomain{scoped: true, prefixes: []string{"adapters/redis", "adapters/postgres"}},
		},
		{
			// StandaloneModule patterns are relative to a separate fixture
			// module, not the workspace, so they cannot map to a repo-relative
			// prefix → unknown (always run), never a never-matching prefix.
			name: "standalone module is unknown",
			body: `func TestSM(t *testing.T) {
	Run(t, StandaloneModule(fixtureDir, TypedOpts{}, []string{"./promoted_ok", "./base"}), nil)
}`,
			fn:   "TestSM",
			want: fileDomain{}, // unknown
		},
		{
			// A computed scope (ModuleScope) contaminates the whole file to
			// unknown even when a resolvable Typed scope is also present.
			name: "modulescope contaminates production into unknown",
			body: `func TestMix(t *testing.T) {
	Run(t, Production(TypedOpts{}), nil)
	Run(t, AST(ModuleScope(root)), nil)
}`,
			fn:   "TestMix",
			want: fileDomain{}, // sawComputed overrides productionGo
		},
		{
			// An unresolved const reference in the patterns slice is computed →
			// unknown (the const is not a package-level string const here).
			name: "unresolved const ref is unknown",
			body: `func TestUnres(t *testing.T) {
	Run(t, AST(DirsScope(root, []string{undefinedConst})), nil)
}`,
			fn:   "TestUnres",
			want: fileDomain{}, // unknown
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := domainOf(t, map[string]string{"rule_test.go": wrapRule(tc.body)}, tc.fn)
			assert.Equal(t, tc.want.scoped, got.scoped, "scoped")
			assert.Equal(t, tc.want.productionGo, got.productionGo, "productionGo")
			assert.ElementsMatch(t, tc.want.prefixes, got.prefixes, "prefixes")
		})
	}
}

// TestBuildFileDomainIndex_ProductionAndFixtureUnion verifies a file that has
// both a real Production scan and a Fixture RED test yields a union domain.
func TestBuildFileDomainIndex_ProductionAndFixtureUnion(t *testing.T) {
	body := `func TestRule(t *testing.T) {
	Run(t, Production(TypedOpts{Tests: false}), nil)
	Run(t, Fixture(FixtureOpts{}, []string{"./tools/archtest/internal/rulefixture/..."}), nil)
}`
	got := domainOf(t, map[string]string{"rule_test.go": wrapRule(body)}, "TestRule")
	assert.True(t, got.scoped)
	assert.True(t, got.productionGo)
	assert.ElementsMatch(t, []string{"tools/archtest/internal/rulefixture"}, got.prefixes)
}

// TestBuildFileDomainIndex_CompanionDispatch verifies that scope declared in a
// companion CheckXxx func reached via Report(t, rule, CheckXxx(...)) is
// attributed back to the dispatching test func (the dominant archtest shape).
func TestBuildFileDomainIndex_CompanionDispatch(t *testing.T) {
	testFile := wrapRule(`func TestAdapter(t *testing.T) { Report(t, adapterRule, CheckAdapter(t)) }`)
	companion := `//go:build archtest

package archtest

import "testing"

func CheckAdapter(t *testing.T) []Diagnostic {
	return Run(t, Typed(TypedOpts{}, []string{"./adapters/redis/..."}), nil)
}
`
	got := domainOf(t, map[string]string{
		"adapter_test.go": testFile,
		"adapter.go":      companion,
	}, "TestAdapter")
	assert.True(t, got.scoped)
	assert.ElementsMatch(t, []string{"adapters/redis"}, got.prefixes)
}

// TestBuildFileDomainIndex_HelperResolution verifies resolution of a dir-set
// helper that returns a slice built from a string const + append (mirrors the
// real platformAndExampleCellScanDirs shape).
func TestBuildFileDomainIndex_HelperResolution(t *testing.T) {
	testFile := wrapRule(`func TestCell(t *testing.T) { Run(t, AST(DirsScope(root, cellDirs())), nil) }`)
	helpers := `//go:build archtest

package archtest

const platformDir = "corecells"

func cellBase() []string { return []string{platformDir} }

func cellDirs() []string { return append(cellBase(), "examples") }
`
	got := domainOf(t, map[string]string{
		"cell_test.go": testFile,
		"cell_dirs.go": helpers,
	}, "TestCell")
	assert.True(t, got.scoped)
	assert.ElementsMatch(t, []string{"corecells", "examples"}, got.prefixes)
}

// TestBuildFileDomainIndex_MissingFuncIsUnknown verifies the safety default:
// a test func absent from the index resolves to the zero-value (unknown)
// domain, which always runs.
func TestBuildFileDomainIndex_MissingFuncIsUnknown(t *testing.T) {
	root := makeFakeArchtestDir(t, map[string]string{
		"rule_test.go": wrapRule(`func TestKnown(t *testing.T) {}`),
	})
	idx, err := buildFileDomainIndex(root)
	require.NoError(t, err)

	d := idx["TestDoesNotExist"]
	assert.False(t, d.scoped, "absent func must resolve to unknown (zero value)")
	assert.True(t, domainSelectsChange(d, "anything/at/all.go"), "unknown must always run")
}

// TestBuildFileDomainIndex_ParseErrorIsUnknownNotFatal verifies that an
// unparseable file does not fail the whole index; its funcs are simply absent
// (=> unknown => always run).
func TestBuildFileDomainIndex_ParseErrorIsUnknownNotFatal(t *testing.T) {
	root := makeFakeArchtestDir(t, map[string]string{
		"good_test.go": wrapRule(`func TestGood(t *testing.T) { Run(t, Production(TypedOpts{}), nil) }`),
		"bad_test.go":  "package archtest\nfunc TestBad( { this is not valid go",
	})
	idx, err := buildFileDomainIndex(root)
	require.NoError(t, err, "a single unparseable file must not fail the index build")
	assert.True(t, idx["TestGood"].productionGo, "parseable files must still resolve")
}
