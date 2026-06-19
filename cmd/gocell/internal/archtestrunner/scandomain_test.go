package archtestrunner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
// archtest source → derived fileDomain. Source need only parse (the runner
// AST-parses tools/archtest; it never type-checks it), so scope constructors
// are referenced as bare identifiers without importing the archtest package.
func TestBuildFileDomainIndex_Extraction(t *testing.T) {
	const header = "//go:build archtest\n\n// INVARIANT: X-01\n\npackage archtest\n\nimport \"testing\"\n\n"

	cases := []struct {
		name string
		src  string
		fn   string
		want fileDomain
	}{
		{
			name: "production scope",
			src:  header + "func TestProd(t *testing.T) { Run(t, Production(TypedOpts{Tests: false}), nil) }\n",
			fn:   "TestProd",
			want: fileDomain{scoped: true, productionGo: true},
		},
		{
			name: "typed literal prefix",
			src:  header + "func TestRedis(t *testing.T) { Run(t, Typed(TypedOpts{}, []string{\"./adapters/redis/...\"}), nil) }\n",
			fn:   "TestRedis",
			want: fileDomain{scoped: true, prefixes: []string{"adapters/redis"}},
		},
		{
			name: "dirsscope literal via scope var",
			src:  header + "func TestAsm(t *testing.T) { scope := DirsScope(root, []string{\"framework/kernel/assembly\"}); Run(t, AST(scope), nil) }\n",
			fn:   "TestAsm",
			want: fileDomain{scoped: true, prefixes: []string{"framework/kernel/assembly"}},
		},
		{
			name: "scanner-qualified dirsscope literal",
			src:  header + "func TestCg(t *testing.T) { Run(t, AST(scanner.DirsScope(root, []string{\"tools/codegen/contractgen\"})), nil) }\n",
			fn:   "TestCg",
			want: fileDomain{scoped: true, prefixes: []string{"tools/codegen/contractgen"}},
		},
		{
			name: "computed typed var is unknown",
			src:  header + "func TestVar(t *testing.T) { pkgs := loadPkgs(); Run(t, Typed(TypedOpts{}, pkgs), nil) }\n",
			fn:   "TestVar",
			want: fileDomain{}, // unknown
		},
		{
			name: "modulescope whole-module is unknown",
			src:  header + "func TestMod(t *testing.T) { Run(t, AST(ModuleScope(root)), nil) }\n",
			fn:   "TestMod",
			want: fileDomain{}, // unknown
		},
		{
			name: "glob mid-path is unknown",
			src:  header + "func TestGlob(t *testing.T) { Run(t, Typed(TypedOpts{}, []string{\"./foo/*/bar\"}), nil) }\n",
			fn:   "TestGlob",
			want: fileDomain{}, // unknown
		},
		{
			name: "no scope is unknown",
			src:  header + "func TestNone(t *testing.T) { _ = 1 }\n",
			fn:   "TestNone",
			want: fileDomain{}, // unknown
		},
		{
			name: "p.Typed() method does not collide with Production",
			src:  header + "func TestColl(t *testing.T) { Run(t, Production(TypedOpts{}), func(p *Pass) []Diagnostic { if !p.Typed() { return nil }; return nil }) }\n",
			fn:   "TestColl",
			want: fileDomain{scoped: true, productionGo: true},
		},
		{
			name: "fixture-only prefix",
			src:  header + "func TestFix(t *testing.T) { Run(t, Fixture(FixtureOpts{}, []string{\"./tools/archtest/internal/foofixture/...\"}), nil) }\n",
			fn:   "TestFix",
			want: fileDomain{scoped: true, prefixes: []string{"tools/archtest/internal/foofixture"}},
		},
		{
			name: "multiple typed literals union",
			src:  header + "func TestMulti(t *testing.T) { Run(t, Typed(TypedOpts{}, []string{\"./adapters/redis/...\", \"./adapters/postgres/...\"}), nil) }\n",
			fn:   "TestMulti",
			want: fileDomain{scoped: true, prefixes: []string{"adapters/redis", "adapters/postgres"}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := domainOf(t, map[string]string{"rule_test.go": tc.src}, tc.fn)
			assert.Equal(t, tc.want.scoped, got.scoped, "scoped")
			assert.Equal(t, tc.want.productionGo, got.productionGo, "productionGo")
			assert.ElementsMatch(t, tc.want.prefixes, got.prefixes, "prefixes")
		})
	}
}

// TestBuildFileDomainIndex_ProductionAndFixtureUnion verifies a file that has
// both a real Production scan and a Fixture RED test yields a union domain.
func TestBuildFileDomainIndex_ProductionAndFixtureUnion(t *testing.T) {
	src := `//go:build archtest

// INVARIANT: X-01

package archtest

import "testing"

func TestRule(t *testing.T) {
	Run(t, Production(TypedOpts{Tests: false}), nil)
	Run(t, Fixture(FixtureOpts{}, []string{"./tools/archtest/internal/rulefixture/..."}), nil)
}
`
	got := domainOf(t, map[string]string{"rule_test.go": src}, "TestRule")
	assert.True(t, got.scoped)
	assert.True(t, got.productionGo)
	assert.ElementsMatch(t, []string{"tools/archtest/internal/rulefixture"}, got.prefixes)
}

// TestBuildFileDomainIndex_CompanionDispatch verifies that scope declared in a
// companion CheckXxx func reached via Report(t, rule, CheckXxx(...)) is
// attributed back to the dispatching test func (the dominant archtest shape).
func TestBuildFileDomainIndex_CompanionDispatch(t *testing.T) {
	testFile := `//go:build archtest

// INVARIANT: ADAPTER-01

package archtest

import "testing"

func TestAdapter(t *testing.T) { Report(t, adapterRule, CheckAdapter(t)) }
`
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
	testFile := `//go:build archtest

// INVARIANT: CELL-01

package archtest

import "testing"

func TestCell(t *testing.T) { Run(t, AST(DirsScope(root, cellDirs())), nil) }
`
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
		"rule_test.go": "//go:build archtest\n\n// INVARIANT: X-01\n\npackage archtest\n\nimport \"testing\"\n\nfunc TestKnown(t *testing.T) {}\n",
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
		"good_test.go": "//go:build archtest\n\n// INVARIANT: G-01\n\npackage archtest\n\nimport \"testing\"\n\nfunc TestGood(t *testing.T) { Run(t, Production(TypedOpts{}), nil) }\n",
		"bad_test.go":  "package archtest\nfunc TestBad( { this is not valid go",
	})
	idx, err := buildFileDomainIndex(root)
	require.NoError(t, err, "a single unparseable file must not fail the index build")
	assert.True(t, idx["TestGood"].productionGo, "parseable files must still resolve")
}
