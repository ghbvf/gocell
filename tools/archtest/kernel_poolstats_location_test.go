// KERNEL-POOLSTATS-LOCATION-01
//
// Invariants:
//
//	01a — `runtime/observability/poolstats` is forbidden as an import path.
//	      The pool-stats Statter is a contract-only package (zero imports,
//	      pure Snapshot + Statter interface) consumed by adapters and
//	      produced by adapters; it lives at `kernel/observability/poolstats`.
//	      Once descended (M0-FOUNDATION), the old runtime path must never
//	      come back — including via type alias or re-export shim.
//
//	01b — `kernel/observability/poolstats` must be import-zero (stdlib only).
//	      The package is a contract: a Snapshot value type and a Statter
//	      interface. Bringing in errcode / pkg / yaml.v3 would couple the
//	      contract to runtime concerns and re-create the very layer
//	      entanglement that motivated the descent.
//
// Refs: docs/backlog.md M0-FOUNDATION
package archtest

import (
	"fmt"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	poolstatsForbiddenImport = "github.com/ghbvf/gocell/runtime/observability/poolstats"
	poolstatsCanonicalDir    = "kernel/observability/poolstats"
)

// TestKERNEL_POOLSTATS_LOCATION_01a_NoLegacyImport walks every .go file in
// the module (production + tests) and fails when any file imports the legacy
// `runtime/observability/poolstats` path.
func TestKERNEL_POOLSTATS_LOCATION_01a_NoLegacyImport(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	var violations []string
	walkPoolstatsModuleGoFiles(t, root, func(rel, path string) {
		// Skip the archtest itself — it has to name the forbidden path
		// to enforce the rule.
		if rel == "tools/archtest/kernel_poolstats_location_test.go" {
			return
		}
		fset := token.NewFileSet()
		data, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			return
		}
		f, err := parser.ParseFile(fset, path, data, parser.ImportsOnly)
		if err != nil {
			return
		}
		for _, imp := range f.Imports {
			if imp.Path == nil {
				continue
			}
			imported := strings.Trim(imp.Path.Value, `"`)
			if imported == poolstatsForbiddenImport {
				pos := fset.Position(imp.Pos())
				violations = append(violations,
					rel+":"+strconv.Itoa(pos.Line)+
						`: imports "`+poolstatsForbiddenImport+
						`" — descend to "github.com/ghbvf/gocell/`+poolstatsCanonicalDir+`"`,
				)
			}
		}
	})

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("KERNEL-POOLSTATS-LOCATION-01a: %s", v)
	}
}

// TestKERNEL_POOLSTATS_LOCATION_01b_ContractIsImportZero scans every
// production .go file under kernel/observability/poolstats/ and fails when
// any non-stdlib import is found. Stdlib paths are identified by the absence
// of a `.` in their first segment (Go community convention: stdlib packages
// have no domain).
func TestKERNEL_POOLSTATS_LOCATION_01b_ContractIsImportZero(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)
	pkgDir := filepath.Join(root, poolstatsCanonicalDir)

	if _, err := os.Stat(pkgDir); os.IsNotExist(err) {
		// Pre-descent: kernel directory not yet populated. The 01a rule
		// keeps the old path forbidden; once descent lands, this test
		// activates automatically.
		t.Skipf("KERNEL-POOLSTATS-LOCATION-01b: %s does not exist yet (pre-descent)", poolstatsCanonicalDir)
		return
	}

	entries, err := os.ReadDir(pkgDir)
	if err != nil {
		t.Fatalf("read %s: %v", pkgDir, err)
	}

	var violations []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(pkgDir, name)
		fset := token.NewFileSet()
		data, err := os.ReadFile(filepath.Clean(path))
		if err != nil {
			continue
		}
		f, err := parser.ParseFile(fset, path, data, parser.ImportsOnly)
		if err != nil {
			continue
		}
		for _, imp := range f.Imports {
			if imp.Path == nil {
				continue
			}
			imported := strings.Trim(imp.Path.Value, `"`)
			if isPoolstatsStdlibImport(imported) {
				continue
			}
			pos := fset.Position(imp.Pos())
			violations = append(violations,
				poolstatsCanonicalDir+"/"+name+":"+strconv.Itoa(pos.Line)+
					`: non-stdlib import "`+imported+
					`" — pool-stats contract must remain import-zero`,
			)
		}
	}

	sort.Strings(violations)
	for _, v := range violations {
		t.Errorf("KERNEL-POOLSTATS-LOCATION-01b: %s", v)
	}
}

// walkPoolstatsModuleGoFiles invokes fn for every .go file under root, skipping
// vendor / worktrees / testdata / .git / node_modules / generated.
func walkPoolstatsModuleGoFiles(t *testing.T, root string, fn func(rel, path string)) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", path, err)
		}
		if d.IsDir() {
			switch d.Name() {
			case "vendor", "worktrees", "testdata", ".git", "node_modules", "generated":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		fn(rel, path)
		return nil
	})
	require.NoError(t, err, "walkPoolstatsModuleGoFiles")
}

// isPoolstatsStdlibImport returns true when imported has no domain segment —
// i.e. its first slash-delimited segment contains no '.'. Go stdlib packages
// like "context", "go/ast", "encoding/json" all satisfy this; module-style
// paths like "github.com/x/y" or "gopkg.in/yaml.v3" do not.
func isPoolstatsStdlibImport(imported string) bool {
	first := imported
	if i := strings.Index(imported, "/"); i >= 0 {
		first = imported[:i]
	}
	return !strings.Contains(first, ".")
}
