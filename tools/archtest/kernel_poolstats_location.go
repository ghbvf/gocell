package archtest

import "strings"

// kernel_poolstats_location.go — importable KERNEL-POOLSTATS-LOCATION-01
// rule constants and shared scanner helpers.
//
// Platform symbol paths are anchored to PlatformModulePath so a module rename
// updates exactly one place. The test entry points
// (TestKERNEL_POOLSTATS_LOCATION_01a_NoLegacyImport,
// TestKERNEL_POOLSTATS_LOCATION_01b_ContractIsImportZero, and the
// ScannerDetectsViolation reverse self-check) live in the _test.go file.
//
// This file is NOT registered in StandardCellRules(): the rule reasons about
// GoCell's own internal package layout, not about how a consumer uses platform
// APIs — it is vacuous-green or false-red in an external module.

const (
	// poolstatsForbiddenImport is the legacy path that must not be imported.
	poolstatsForbiddenImport = PlatformFrameworkModulePath + "/runtime/observability/poolstats"

	// poolstatsCanonicalDir is the correct location after the M0-FOUNDATION descent,
	// workspace-root-relative (so it doubles as the on-disk DirsScope path AND the
	// PlatformModulePath+"/"+dir import-path suffix). Post-#1565 kernel lives under
	// framework/, so the dir carries the framework/ segment.
	poolstatsCanonicalDir = "framework/kernel/observability/poolstats"
)

// scanPoolstatsNonStdlibImports scans every file in the pass for non-stdlib
// imports and returns one Diagnostic per offending import path. It is used by
// TestKERNEL_POOLSTATS_LOCATION_01b_ContractIsImportZero and its sibling
// ScannerDetectsViolation reverse self-check.
func scanPoolstatsNonStdlibImports(p *Pass) []Diagnostic {
	var ds []Diagnostic
	for _, file := range p.Files {
		for _, imp := range file.Imports {
			if imp.Path == nil {
				continue
			}
			imported := strings.Trim(imp.Path.Value, `"`)
			if isPoolstatsStdlibImport(imported) {
				continue
			}
			ds = append(ds, Diagnostic{
				Rel:     p.Rel(file),
				Line:    p.Fset.Position(imp.Path.Pos()).Line,
				Message: `non-stdlib import "` + imported + `" — pool-stats contract must remain import-zero`,
			})
		}
	}
	return ds
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
