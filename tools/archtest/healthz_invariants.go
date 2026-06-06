package archtest

// healthz_invariants.go — importable HEALTHZ-WRITE-01 rule constants.
//
// Platform symbol paths are anchored to PlatformModulePath so a module rename
// updates exactly one place. The scanner logic (scanHealthzA1, scanHealthzA3,
// fieldTypeIsAggregator, etc.) lives in the _test.go file because it is not
// needed by external Cell repos; only the path constants need to be in a
// non-test file for importability.
//
// This file is NOT registered in StandardCellRules(): the rule reasons about
// how cells consume platform infra types via healthz.Aggregator; it depends on
// the full scanner infrastructure (types.Named + typesutil) that is suitable
// for dogfood runs but whose scanner functions currently live in the _test.go.

import "strings"

const (
	// healthzAggPkgPath is the import path of the kernel/healthz package that
	// defines the Aggregator interface (used by A1 and A3 detection).
	healthzAggPkgPath = PlatformModulePath + "/kernel/healthz"
)

// healthzHolderAllowlist is the set of (pkg-path.TypeName) pairs allowed to
// hold a healthz.Aggregator field (HEALTHZ-WRITE-01/A3).
var healthzHolderAllowlist = map[string]bool{
	PlatformModulePath + "/runtime/observability/healthz.aggregator": true, // unexported impl
	PlatformModulePath + "/runtime/http/health.Handler":              true, // HTTP transport
	PlatformModulePath + "/runtime/bootstrap.Bootstrap":              true, // composition root
}

// healthzAllLayerPrefixes is the set of platform layer prefixes scanned by
// TestHealthzWrite01 (A1 + A3 production scan): kernel, cells, adapters,
// runtime, cmd, examples.
var healthzAllLayerPrefixes = []string{
	PlatformModulePath + "/kernel/",
	PlatformModulePath + "/cells/",
	PlatformModulePath + "/adapters/",
	PlatformModulePath + "/runtime/",
	PlatformModulePath + "/cmd/",
	PlatformModulePath + "/examples/",
}

// healthzNonKernelLayerPrefixes is the set of platform layer prefixes scanned
// by TestHealthzInvariants_ReverseBlindSpot_NoDynamicHealthzPath (B-A1 blind-spot
// check): cells, adapters, runtime, cmd, examples — intentionally excludes
// kernel/ which is not part of that check.
var healthzNonKernelLayerPrefixes = []string{
	PlatformModulePath + "/cells/",
	PlatformModulePath + "/adapters/",
	PlatformModulePath + "/runtime/",
	PlatformModulePath + "/cmd/",
	PlatformModulePath + "/examples/",
}

// healthzHasAnyPrefix reports whether pkgPath starts with any of the given
// prefixes.
func healthzHasAnyPrefix(pkgPath string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(pkgPath, p) {
			return true
		}
	}
	return false
}
