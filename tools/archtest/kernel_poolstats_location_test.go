// INVARIANT: KERNEL-POOLSTATS-LOCATION-01a
// INVARIANT: KERNEL-POOLSTATS-LOCATION-01b
//
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
	"go/parser"
	"strings"
	"testing"

	"github.com/ghbvf/gocell/tools/archtest/internal/scanner"
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
	scanner.ImportBan{
		RuleID:    "KERNEL-POOLSTATS-LOCATION-01a",
		Forbidden: []string{poolstatsForbiddenImport},
		AllowRels: []string{"tools/archtest/kernel_poolstats_location_test.go"},
		Hint:      `descend to "github.com/ghbvf/gocell/` + poolstatsCanonicalDir + `"`,
	}.Run(t, scanner.ModuleScope(root, scanner.IncludeTests()))
}

// TestKERNEL_POOLSTATS_LOCATION_01b_ContractIsImportZero scans every
// production .go file under kernel/observability/poolstats/ and fails when
// any non-stdlib import is found. Stdlib paths are identified by the absence
// of a `.` in their first segment (Go community convention: stdlib packages
// have no domain).
//
// Pre-descent: if kernel/observability/poolstats does not exist yet, DirsScope
// returns an empty file set and this test passes vacuously (no files to scan,
// no violations possible). This is correct — the constraint is vacuously true
// before the directory exists; 01a keeps the old path forbidden in the meantime.
func TestKERNEL_POOLSTATS_LOCATION_01b_ContractIsImportZero(t *testing.T) {
	t.Parallel()
	root := findModuleRoot(t)

	var diags []scanner.Diagnostic
	scope := scanner.DirsScope(root, []string{poolstatsCanonicalDir})
	scanner.EachFile(t, scope, parser.ImportsOnly, func(t *testing.T, fc scanner.FileContext) {
		for _, imp := range fc.File.Imports {
			if imp.Path == nil {
				continue
			}
			imported := strings.Trim(imp.Path.Value, `"`)
			if isPoolstatsStdlibImport(imported) {
				continue
			}
			diags = append(diags, scanner.Diagnostic{
				Rel:     fc.Rel,
				Line:    fc.Fset.Position(imp.Path.Pos()).Line,
				Message: `non-stdlib import "` + imported + `" — pool-stats contract must remain import-zero`,
			})
		}
	})
	scanner.Report(t, "KERNEL-POOLSTATS-LOCATION-01b", diags)
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
