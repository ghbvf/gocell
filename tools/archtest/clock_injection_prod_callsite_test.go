// CLOCK-INJECTION-PROD-CALLSITE-01 — production composition-root gate for
// Clock injection.
//
// Invariant: In every production composition-root file (cmd/**/*.go and
// examples/**/main.go), any call to a constructor that accepts variadic
// Option parameters and whose package exports a WithClock(Clock) Option
// function must pass WithClock(...) as one of the option arguments.
//
// This is the production-side complement to CLOCK-INJECTION-TEST-CALLSITE-01
// which only scans *_test.go files. Together they enforce clock injection
// at every composition boundary.
//
// Scope: cmd/ top-level packages and examples/**/main.go files (i.e. the
// composition roots that wire together all cells and bootstrap). Intentionally
// NOT scanning cells/, runtime/, kernel/ — those packages are the injection
// targets, not the composition roots.
//
// Exemption: when Go syntax prevents passing WithClock as a direct arg,
// annotate the call site with `//archtest:allow:clock-injection:via-slice
// <reason>` on the same line as the closing `)`. The reason field is mandatory.
//
// ref: docs/architecture/202605021500-adr-kernel-clock-injection.md
// ref: docs/plans/202605011500-029-master-roadmap.md Track D #D6
// ref: uber-go/fx fx.Provide DI graph validation
package archtest

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"

	"github.com/ghbvf/gocell/tools/archtest/internal/typeseval"
	"github.com/ghbvf/gocell/tools/internal/fileroles"
)

// isCompositionRoot reports whether the given module-relative path is a
// production composition-root file for CLOCK-INJECTION-PROD-CALLSITE-01.
//
// Composition roots are:
//   - any non-test .go file under cmd/
//   - any main.go file under examples/ at any depth
//
// Intentionally NOT flagging cells/, runtime/, kernel/ — those are injection
// targets, not composition roots.
func isCompositionRoot(rel string) bool {
	if rel == "" {
		return false
	}
	if strings.HasSuffix(rel, "_test.go") {
		return false
	}
	if strings.HasPrefix(rel, "tools/archtest/") {
		return false
	}
	if strings.Contains(rel, "/testdata/") || strings.HasPrefix(rel, "testdata/") {
		return false
	}
	// cmd/: all non-test Go files
	if strings.HasPrefix(rel, "cmd/") {
		return true
	}
	// examples/: only main.go files (composition roots, not library code)
	if strings.HasPrefix(rel, "examples/") && strings.HasSuffix(rel, "/main.go") {
		return true
	}
	return false
}

// compositionRootDirExists checks whether a directory exists under root.
func compositionRootDirExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
}

// TestClockInjectionProdCallsite enforces CLOCK-INJECTION-PROD-CALLSITE-01:
// production composition-root files (cmd/ + examples/*/main.go) must pass
// WithClock when calling constructors whose package exports WithClock.
func TestClockInjectionProdCallsite(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("skipping packages.Load-based archtest in -short mode")
	}

	root := findModuleRoot(t)

	// Build patterns covering only cmd/ and examples/ (the composition roots).
	// We deliberately exclude cells/runtime/kernel/ — those are injection targets.
	var patterns []string
	for _, dir := range []string{"cmd", "examples"} {
		if compositionRootDirExists(filepath.Join(root, dir)) {
			patterns = append(patterns, "./"+dir+"/...")
		}
	}
	if len(patterns) == 0 {
		t.Skip("no cmd/ or examples/ directories found")
	}

	// Load without tests=true — composition root files are not test files.
	pkgs, errs, err := typeseval.LoadPackages(root, false, nil, patterns...)
	require.NoError(t, err, "packages.Load failed")
	require.Empty(t, errs, "package load errors must fail-closed: %v", errs)

	// Reuse collectClockRequiredCtors from the test-callsite gate — same
	// detection logic (New* with variadic params + WithClock in same package).
	ctors := collectClockRequiredCtors(pkgs)

	var violations []string
	visited := map[string]bool{}

	packages.Visit(pkgs, nil, func(p *packages.Package) {
		for i, file := range p.Syntax {
			if i >= len(p.GoFiles) {
				continue
			}
			abs := p.GoFiles[i]
			if visited[abs] {
				continue
			}
			visited[abs] = true

			rel, ok := fileroles.Rel(root, abs)
			if !ok {
				continue
			}
			if !isCompositionRoot(rel) {
				continue
			}

			violations = append(violations,
				scanClockCallsiteAST(p.Fset, file, rel, p.TypesInfo, ctors)...)
		}
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Log(v)
	}
	assert.Empty(t, violations,
		"CLOCK-INJECTION-PROD-CALLSITE-01: production composition-root files "+
			"(cmd/ + examples/*/main.go) must pass WithClock(clk) when calling "+
			"constructors that enforce Clock injection. "+
			"ref: docs/architecture/202605021500-adr-kernel-clock-injection.md")
}
