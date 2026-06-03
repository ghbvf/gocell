// INVARIANT: PACKAGES-CONFIG-GOWORK-OFF-01
//
// Every golang.org/x/tools/go/packages.Config composite literal in the repo
// MUST set an Env field whose value contains the string literal "GOWORK=off".
//
// WHY: PR #1554 commits a repo-root go.work (`use .`). With go.work present the
// `go` tool runs in workspace mode everywhere under the tree, and packages.Load
// rejects any directory whose module is not listed in `use` — e.g. the isolated
// fixture modules under tools/archtest/testdata/* and tools/depgraph/testdata/*
// ("directory ... does not contain modules listed in go.work"). Forcing
// GOWORK=off keeps every package loader go.work-agnostic (module mode), loading
// the root module and standalone fixtures exactly as in the pre-go.work world.
// This is the static-guard arm of "introducing a constraint must close it in the
// same PR": go.work's repo-wide effect made GOWORK=off a loader invariant.
//
// RATING: Medium. Type-aware (go/types resolves the literal to the canonical
// golang.org/x/tools/go/packages.Config, immune to import aliases) + form-locked
// (Env must carry an inline "GOWORK=off" string literal). Not Hard: a loader
// could set Env outside the composite literal, build it via a helper, or
// construct packages.Config as a zero value + field assignment — see BLIND SPOTS.
// A Hard upgrade (sealing packages.Config construction behind one repo helper)
// is not pursued: packages.Config is an external (x/tools) type the repo cannot
// seal. Functional regressions in the real loaders are independently caught by
// their own tests (tools/depgraph/*_test.go and the archtest fixture suites fail
// to load under go.work without GOWORK=off).
//
// BLIND SPOTS (per ai-robust.md; each has a reverse self-check below or is an
// accepted Medium gap):
//  1. Env set via a separate `cfg.Env = …` assignment rather than inside the
//     composite literal — NOT detected. Accepted: all repo loaders set Env
//     inline; requiring inline is the form-lock.
//  2. Env value referencing a helper/var instead of an inline "GOWORK=off"
//     string literal (e.g. `Env: goworkOffEnv()`) — NOT detected. Accepted gap.
//  3. packages.Config built as a zero value + field writes (`var c packages.Config`)
//     rather than a composite literal — NOT detected. Accepted: no repo loader
//     uses that form; a functional break would surface in the loader's own test.
//
// REVERSE SELF-CHECKS:
//   - Anti-vacuity: the production scan asserts it resolved >= a known number of
//     real packages.Config literals, proving type resolution is non-vacuous.
//   - Predicate self-check (TestPackagesConfigGoworkOff01Predicate): exercises
//     compositeLitHasGoworkOffEnv on RED/GREEN parsed literals, proving the
//     Env-detection half fires on the absent / wrong-value forms.
package archtest

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strconv"
	"sync"
	"testing"
)

const (
	xtoolsPackagesPath  = "golang.org/x/tools/go/packages"
	goworkOffEnvLiteral = "GOWORK=off"
	packagesConfigRule  = "PACKAGES-CONFIG-GOWORK-OFF-01"

	// minPackagesConfigLiterals is the anti-vacuity floor. The repo currently
	// has 6 packages.Config literals (typeseval, depgraph build.go + build_test.go,
	// check.go ×2, metricschema), and the typed Tests=true scan visits production
	// files in multiple package variants, so the observed count is >= 6. A floor
	// of 4 stays clear of vacuity while tolerating loader churn; if the scan ever
	// returns fewer, type resolution has silently broken.
	minPackagesConfigLiterals = 4
)

// TestPackagesConfigGoworkOff01 scans every repo packages.Config composite
// literal (production + test variants, generated/ excluded) and fails for any
// that does not set Env containing "GOWORK=off".
func TestPackagesConfigGoworkOff01(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	var (
		mu          sync.Mutex
		configCount int
	)
	diags := Run(t, Production(TypedOpts{Tests: true}), func(p *Pass) []Diagnostic {
		var d []Diagnostic
		for _, f := range p.Files {
			rel := p.Rel(f)
			EachInSubtree[ast.CompositeLit](f, func(lit *ast.CompositeLit) {
				if !isXToolsPackagesConfig(p.TypesInfo, lit) {
					return
				}
				mu.Lock()
				configCount++
				mu.Unlock()
				if compositeLitHasGoworkOffEnv(lit) {
					return
				}
				pos := p.Fset.Position(lit.Pos())
				d = append(d, Diagnostic{
					Rel:  rel,
					Line: pos.Line,
					Message: fmt.Sprintf(
						packagesConfigRule+": %s:%d constructs golang.org/x/tools/go/packages.Config "+
							"without Env containing %q. Add `Env: append(os.Environ(), %q)` to keep the "+
							"loader go.work-agnostic; workspace mode otherwise rejects standalone fixture "+
							"modules. See tools/archtest/internal/typeseval for the canonical form.",
						rel, pos.Line, goworkOffEnvLiteral, goworkOffEnvLiteral,
					),
				})
			})
		}
		return d
	})

	for _, diag := range diags {
		t.Errorf("%s", diag.Message)
	}
	if configCount < minPackagesConfigLiterals {
		t.Fatalf("anti-vacuity: PACKAGES-CONFIG-GOWORK-OFF-01 resolved only %d packages.Config "+
			"literal(s); expected >= %d. Type resolution may have silently broken.",
			configCount, minPackagesConfigLiterals)
	}
}

// isXToolsPackagesConfig reports whether lit's type is the canonical
// golang.org/x/tools/go/packages.Config named type. Uses go/types so import
// aliases and dot-imports cannot disguise the type.
func isXToolsPackagesConfig(info *types.Info, lit *ast.CompositeLit) bool {
	if info == nil {
		return false
	}
	t := info.TypeOf(lit)
	if t == nil {
		return false
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	obj := named.Obj()
	return obj != nil && obj.Pkg() != nil &&
		obj.Pkg().Path() == xtoolsPackagesPath && obj.Name() == "Config"
}

// compositeLitHasGoworkOffEnv reports whether lit has a keyed `Env:` element
// whose value subtree contains an inline "GOWORK=off" string literal. Uses the
// scanner walk helpers (not a raw for-range over Elts) to avoid self-triggering
// SCANNER-FRAMEWORK-USAGE-01.
func compositeLitHasGoworkOffEnv(lit *ast.CompositeLit) bool {
	found := false
	EachInChildren[ast.KeyValueExpr](lit, func(kv *ast.KeyValueExpr) {
		key, ok := kv.Key.(*ast.Ident)
		if !ok || key.Name != "Env" {
			return
		}
		if exprContainsStringLiteral(kv.Value, goworkOffEnvLiteral) {
			found = true
		}
	})
	return found
}

// exprContainsStringLiteral reports whether expr's subtree contains a STRING
// BasicLit whose unquoted value equals want.
func exprContainsStringLiteral(expr ast.Expr, want string) bool {
	found := false
	EachInSubtree[ast.BasicLit](expr, func(bl *ast.BasicLit) {
		if bl.Kind != token.STRING {
			return
		}
		if s, err := strconv.Unquote(bl.Value); err == nil && s == want {
			found = true
		}
	})
	return found
}

// TestPackagesConfigGoworkOff01Predicate is the reverse self-check for the
// Env-detection half: it asserts compositeLitHasGoworkOffEnv fires correctly on
// RED (missing / wrong Env) and GREEN (GOWORK=off present) parsed literals.
func TestPackagesConfigGoworkOff01Predicate(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want bool
	}{
		{"append form", `packages.Config{Mode: m, Dir: d, Env: append(os.Environ(), "GOWORK=off")}`, true},
		{"slice form", `packages.Config{Env: []string{"GOWORK=off"}}`, true},
		{"trailing append", `packages.Config{Env: append(base, "FOO=1", "GOWORK=off")}`, true},
		{"missing env", `packages.Config{Mode: m, Dir: d}`, false},
		{"wrong value", `packages.Config{Env: append(os.Environ(), "GOFLAGS=-mod=mod")}`, false},
		{"nil env", `packages.Config{Env: nil}`, false},
		{"empty literal", `packages.Config{}`, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			expr, err := parser.ParseExpr(tc.src)
			if err != nil {
				t.Fatalf("parse %q: %v", tc.src, err)
			}
			lit, ok := expr.(*ast.CompositeLit)
			if !ok {
				t.Fatalf("parsed %q is %T, want *ast.CompositeLit", tc.src, expr)
			}
			if got := compositeLitHasGoworkOffEnv(lit); got != tc.want {
				t.Errorf("compositeLitHasGoworkOffEnv(%q) = %v, want %v", tc.src, got, tc.want)
			}
		})
	}
}
