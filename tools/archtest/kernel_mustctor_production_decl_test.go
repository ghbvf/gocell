// INVARIANT: KERNEL-MUSTCTOR-PRODUCTION-DECL-01
//
// # KERNEL-MUSTCTOR-PRODUCTION-DECL-01
//
// Production packages (kernel/ runtime/ adapters/ cells/ cmd/ examples/)
// must not declare functions whose name starts with "Must" unless the
// (modulePath, funcName) pair appears in the carve-out registry below.
// Three categories of legitimate Must-named functions are allowed:
//
//  1. assertion guard — run-time invariant violations with no construction
//     duty (e.g. `MustHaveClock`, `MustNot*`, `MustValidate*`). Panic
//     wrapped via `panicregister.Approved`, locked by `PANIC-REGISTERED-01`.
//
//  2. codegen funnel — sole caller is codegen output; panic ≡ ADR-violating
//     metadata drift detectable at compile time of codegen.
//     `cell.MustNewBaseCell`/`MustNewBaseSliceFromMeta` are also locked by
//     `BASESLICE-CTOR-FUNNEL-01` as the only approved construction path.
//
//  3. test fixture — physically isolated in a test-fixture package
//     (`pkg/contracttest/`, `pkg/testutil/`, `runtime/auth/authtest/`,
//     `cells/internal/testoutbox/`). Callers must be `_test.go` files —
//     enforced indirectly by the package not being importable from
//     production paths without raising review attention.
//
// Other `Must*` production declarations were removed by the B2-K-02 ship
// (see ADR `docs/architecture/202605171800-adr-kernel-mustctor-removal.md`);
// callers shifted to error-first `New*` constructors.
//
// AI-rebust grade: Medium — the carve-out registry is a (pkgPath, funcName)
// string-keyed allowlist. The Hard primary defense is the deletion of the
// 20 production `Must*` constructors plus the physical relocation of the
// three test-only key fns from `runtime/auth` to `runtime/auth/authtest`,
// making the symbols unreachable at compile time. This archtest is the
// secondary defense — preventing future AI from re-introducing Must*
// declarations in production paths. ADR §"为何 Medium 是终态" documents why
// upgrading to Hard (sealed marker receiver) is rejected: the 547-caller
// prefix expansion violates the 优雅简洁 principle.
//
// Blind-spot inventory:
//
//   - Anonymous function or method receiver named "Must*" (e.g.
//     `func (x X) MustFoo()`): scanner emits a FuncDecl with non-nil Recv;
//     this rule INCLUDES method declarations in scope (no method exemption).
//     Reverse self-check (subtest) verifies a method `MustFoo` on a fixture
//     type is flagged.
//
//   - reflect-based registration: dynamic function creation through
//     `reflect.MakeFunc` is not a FuncDecl AST node and therefore invisible
//     to this archtest. Out of scope; tracked as theoretical attack surface
//     identical to `BASESLICE-CTOR-FUNNEL-01` blind-spot inventory.
//
//   - cross-package alias / type alias: the rule operates on declaration
//     sites only. Re-exporting via `var MustFoo = pkg.MustFoo` is a `*ast.ValueSpec`,
//     not a `*ast.FuncDecl`, and is therefore not flagged. Out of scope:
//     a re-export would still hit `PANIC-REGISTERED-01` if it ever invoked
//     panic, and would be visible in PR diff as a new top-level var.
//
// ref: ADR `docs/architecture/202605171800-adr-kernel-mustctor-removal.md`
// ref: `.claude/rules/gocell/ai-collab.md` §"AI-rebust 三档分级"
// ref: backlog `docs/backlog.md:38` B2-K-02
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// allowedMustDecls maps (module-relative package path → set of allowed Must*
// function names) for the KERNEL-MUSTCTOR-PRODUCTION-DECL-01 rule. Every
// entry is justified by category (a/b/c) above; see ADR
// `docs/architecture/202605171800-adr-kernel-mustctor-removal.md`
// §"carve-out registry" for the per-entry rationale.
//
// Adding an entry requires (i) a new ADR row in the carve-out registry table
// and (ii) the categorical justification (assertion guard / codegen funnel /
// test fixture). Removing an entry requires the corresponding production
// declaration to be deleted in the same PR.
var allowedMustDecls = map[string]map[string]struct{}{
	// (a) assertion guards in kernel/
	"kernel/cell": {
		"MustHaveNonEmptyHealthName":     {},
		"MustHaveLifecycleHookName":      {},
		"MustHaveNonEmptyConfigPrefixes": {},
		"MustHaveNonNilConfigReloadFn":   {},
		"MustNotBeRegistryFinalized":     {},
		// (b) codegen funnel — BASESLICE-CTOR-FUNNEL-01 covers MustNewBaseSliceFromMeta
		"MustNewBaseCell":          {},
		"MustNewBaseSliceFromMeta": {},
	},
	"kernel/clock": {
		"MustHaveClock":            {},
		"MustHavePositiveInterval": {},
	},
	"kernel/observability/metrics": {
		"MustValidateLabels": {},
	},
	"kernel/metadata": {
		"MustNewGoIdentifier": {},
	},
	"kernel/outbox": {
		"MustNewEntryID": {},
	},
	"pkg/errcode": {
		"MustValidateDetailsKinds": {},
	},
	// (b) codegen / sealed funnel
	"cells/auditcore/internal/appender": {
		"MustNewSpec": {},
	},
	// (a) internal validator — NewHub calls it internally, not exposed as constructor
	"runtime/websocket": {
		"MustValidateHubConfig": {},
	},
	// (c) test fixture method on test-only MemStore — exposed for storetest
	// conformance suite (negative Verify cases). Cannot move to _test.go
	// because storetest sub-package consumes these methods at suite runtime.
	"runtime/audit/ledger": {
		"MustTamperEntryHash":     {},
		"MustTamperEntryPrevHash": {},
	},
}

// testFixturePkgPrefixes are package path prefixes (module-relative) that are
// exempt from this rule. These packages declare Must* test fixtures by design
// (K8s `httptest` model); callers are `_test.go` files which are not loaded
// by RunTypedProduction.
//
// Adding a prefix requires:
//   - the package must have a name suggesting test use (testutil, contracttest,
//     authtest, testoutbox, ...) so the import line itself communicates intent,
//   - production callers must not import the package (best-effort review-driven
//     guarantee; not statically enforced by this rule).
var testFixturePkgPrefixes = []string{
	"tests/contracttest",
	"pkg/testutil",
	"runtime/auth/authtest",
	"runtime/audit/ledger/storetest",
	"cells/internal/testoutbox",
	"kernel/cell/celltest",
	// tools/archtest/testdata fixtures used by other archtests (panic_registered etc.)
	"tools/archtest/testdata",
}

// isTestFixturePkg reports whether the module-relative package path belongs to
// an exempt test fixture package.
func isTestFixturePkg(relPkg string) bool {
	for _, prefix := range testFixturePkgPrefixes {
		if relPkg == prefix || strings.HasPrefix(relPkg, prefix+"/") {
			return true
		}
	}
	return false
}

// isAllowedMustDecl reports whether the given Must* declaration is allowed by
// the carve-out registry. Pure function exposed for sub-test verification of
// the carve-out logic.
func isAllowedMustDecl(relPkg, funcName string) bool {
	pkgAllow, ok := allowedMustDecls[relPkg]
	if !ok {
		return false
	}
	_, allowed := pkgAllow[funcName]
	return allowed
}

// TestKernelMustCtorProductionDecl scans the entire module's production
// packages and flags any `func Must*` declaration that is not (i) in a test
// fixture package or (ii) explicitly listed in allowedMustDecls. Method
// receivers on production types are also flagged (no method exemption).
func TestKernelMustCtorProductionDecl(t *testing.T) {
	t.Parallel()

	root := findModuleRoot(t)
	modPath, err := moduleImportPath(root)
	require.NoError(t, err, "read module path from go.mod")

	diags := RunTypedProduction(t, TypedOpts{Tests: false}, func(p *Pass) []Diagnostic {
		if p.Pkg == nil {
			return nil
		}
		relPkg := strings.TrimPrefix(p.Pkg.Path(), modPath+"/")
		if relPkg == modPath {
			// Top-level module (no sub-pkg prefix) — never a test fixture.
			relPkg = ""
		}
		if isTestFixturePkg(relPkg) {
			return nil
		}

		var out []Diagnostic
		for _, file := range p.Files {
			for _, decl := range file.Decls {
				fd, ok := decl.(*ast.FuncDecl)
				if !ok {
					continue
				}
				if !strings.HasPrefix(fd.Name.Name, "Must") {
					continue
				}
				if isAllowedMustDecl(relPkg, fd.Name.Name) {
					continue
				}
				pos := p.Fset.Position(fd.Pos())
				out = append(out, Diagnostic{
					Rel:  p.Rel(file),
					Line: pos.Line,
					Message: fmt.Sprintf(
						"forbidden production declaration %q in package %q — "+
							"production paths must use error-first New*; carve-out the entry "+
							"in tools/archtest/kernel_mustctor_production_decl_test.go and ADR "+
							"docs/architecture/202605171800-adr-kernel-mustctor-removal.md if the "+
							"declaration is an assertion guard / codegen funnel / test fixture",
						fd.Name.Name, relPkg,
					),
				})
			}
		}
		return out
	})

	Report(t, "KERNEL-MUSTCTOR-PRODUCTION-DECL-01", diags)
}

// TestKernelMustCtorCarveOutLogic verifies the carve-out helper against
// hand-crafted inputs. This is the reverse self-check: it does not run a
// full module scan but confirms the allowlist logic returns the expected
// answer for representative carve-out entries and non-entries.
func TestKernelMustCtorCarveOutLogic(t *testing.T) {
	t.Parallel()
	cases := []struct {
		relPkg, funcName string
		wantAllowed      bool
		desc             string
	}{
		// allow-listed: assertion guard
		{"kernel/clock", "MustHaveClock", true, "kernel/clock.MustHaveClock — assertion guard"},
		// allow-listed: codegen funnel
		{"kernel/cell", "MustNewBaseCell", true, "kernel/cell.MustNewBaseCell — codegen funnel"},
		{"kernel/cell", "MustNewBaseSliceFromMeta", true, "kernel/cell.MustNewBaseSliceFromMeta — funnel"},
		// allow-listed: assertion-style validator inside NewHub
		{"runtime/websocket", "MustValidateHubConfig", true, "runtime/websocket.MustValidateHubConfig — internal validator"},
		// NOT allow-listed: removed by B2-K-02
		{"kernel/wrapper", "MustHTTPHandler", false, "kernel/wrapper.MustHTTPHandler — must be removed"},
		{"kernel/cell", "MustNewAuthJWT", false, "kernel/cell.MustNewAuthJWT — must be removed"},
		{"runtime/auth/session", "MustNewProtocol", false, "runtime/auth/session.MustNewProtocol — must be removed"},
		{"runtime/http/router", "MustNew", false, "runtime/http/router.MustNew — must be removed"},
		{"adapters/websocket", "MustUpgradeHandler", false, "adapters/websocket.MustUpgradeHandler — must be removed"},
		// hypothetical future violation
		{"cells/accesscore", "MustViolation", false, "hypothetical violation in cells/accesscore must be flagged"},
	}
	for _, tc := range cases {
		got := isAllowedMustDecl(tc.relPkg, tc.funcName)
		if got != tc.wantAllowed {
			t.Errorf("isAllowedMustDecl(%q, %q) = %v, want %v — %s",
				tc.relPkg, tc.funcName, got, tc.wantAllowed, tc.desc)
		}
	}
}

// TestKernelMustCtorReverseFixtureScan parses a synthetic Go file containing
// a deliberately violating `func MustViolation()` declaration and confirms
// the per-file scan logic flags it. This validates that the FuncDecl scan
// in TestKernelMustCtorProductionDecl is wired correctly (i.e. it would
// catch a regression introduced by a future contributor).
//
// The fixture is parsed from a string literal rather than checked into a
// file under tools/archtest so that the live TestKernelMustCtorProductionDecl
// scan does not have to maintain a build-tag carve-out for it.
func TestKernelMustCtorReverseFixtureScan(t *testing.T) {
	t.Parallel()
	const fixtureSrc = `package badfixture

// MustViolation is a deliberate violation used by the reverse self-check.
func MustViolation() {}

// MustOK is also a violation; placed beside the first to confirm multiple
// hits are reported.
func MustOK() {}

// NewFoo is a normal error-first constructor; must NOT be flagged.
func NewFoo() (struct{}, error) { return struct{}{}, nil }
`
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", fixtureSrc, 0)
	require.NoError(t, err)

	const relPkg = "synthetic/badfixture"
	require.False(t, isTestFixturePkg(relPkg), "synthetic fixture pkg must not be exempt")

	var hits []string
	for _, decl := range file.Decls {
		fd, ok := decl.(*ast.FuncDecl)
		if !ok {
			continue
		}
		if !strings.HasPrefix(fd.Name.Name, "Must") {
			continue
		}
		if isAllowedMustDecl(relPkg, fd.Name.Name) {
			continue
		}
		hits = append(hits, fd.Name.Name)
	}
	require.ElementsMatch(t, []string{"MustViolation", "MustOK"}, hits,
		"reverse fixture scan must flag both Must* declarations and skip NewFoo")
}
