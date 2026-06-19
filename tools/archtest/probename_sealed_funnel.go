package archtest

// probename_sealed_funnel.go — platform-path carriers for
// PROBENAME-SEALED-FUNNEL-01.
//
// Every "github.com/ghbvf/gocell/..." path is derived from PlatformModulePath
// so a module rename updates exactly one place (Shape B, M3 #1302).  The
// scanner logic lives in probename_sealed_funnel_test.go; only the path
// constants, sanctioned-package maps, and the in-repo prefix helper live here
// (non-test file, same package — transparent to the _test.go).
//
// Mirrors the shape of tools/archtest/cell_public_option_param.go.

import "strings"

// probenameModPrefix is the module path prefix used to strip the module path
// from package paths and to test whether a package belongs to this module.
const probenameModPrefix = PlatformModulePath + "/"

// healthzPkgPath is the import path of the kernel/healthz package.
// Named without a "probename" prefix because it is the single authoritative
// package for all healthz concepts (ProbeName, Probe, Aggregator).
const healthzPkgPath = PlatformFrameworkModulePath + "/kernel/healthz"

// cellRegistrarPkgPath is the import path of the kernel/cell package,
// which defines the Registrar interface and RegisterReadiness method.
const cellRegistrarPkgPath = PlatformFrameworkModulePath + "/kernel/cell"

// adapterutilPkgPath is the import path of the adapterutil package,
// which provides HealthToProbe (consumes ProbeName as first arg).
const adapterutilPkgPath = PlatformModulePath + "/adapters/adapterutil"

// bootstrapPkgPath is the import path of the runtime/bootstrap package,
// which provides WithHealthChecker (4th sanctioned ProbeName funnel
// ingress — composition-root option pattern; consumes ProbeName as
// first arg).
const bootstrapPkgPath = PlatformFrameworkModulePath + "/runtime/bootstrap"

// probeNameSanctionedPkgs is the closed set of packages allowed to declare
// a healthz.ProbeName typed const.  A const appearing in any other package
// is rejected by A1.
var probeNameSanctionedPkgs = map[string]bool{
	// Framework-level (kernel owns the typed concept)
	PlatformFrameworkModulePath + "/kernel/healthz": true,
	// Adapter dependency probes
	PlatformModulePath + "/adapters/grpc":     true,
	PlatformModulePath + "/adapters/postgres": true,
	PlatformModulePath + "/adapters/redis":    true,
	PlatformModulePath + "/adapters/rabbitmq": true,
	PlatformModulePath + "/adapters/s3":       true,
	PlatformModulePath + "/adapters/vault":    true,
	PlatformModulePath + "/adapters/mqtt":     true,
	PlatformModulePath + "/adapters/oidc":     true,
	// Runtime-level probe owners
	PlatformFrameworkModulePath + "/runtime/outbox":    true,
	PlatformFrameworkModulePath + "/runtime/websocket": true,
	PlatformFrameworkModulePath + "/runtime/saga":      true,
	// Platform cells (cellgen healthz_gen.go — marker required)
	PlatformCellsModulePath + "/configcore":   true,
	PlatformCellsModulePath + "/auditcore":    true,
	PlatformCellsModulePath + "/accesscore":   true,
	PlatformCellsModulePath + "/registrycore": true, // #2237 (303-US6; cellgen-emitted ProbeRepoReady)
	PlatformCellsModulePath + "/syscore":      true, // #1860 (cellgen-emitted ProbeRepoReady; unused — no repo)
	// Example cells (cellgen healthz_gen.go — marker required)
	PlatformModulePath + "/examples/demo/cells/democell":                         true,
	PlatformModulePath + "/examples/iotdevice/cells/devicecell":                  true,
	PlatformModulePath + "/examples/orderfulfillment/cells/orderfulfillmentcell": true,
	PlatformModulePath + "/examples/todoorder/cells/ordercell":                   true,
	PlatformModulePath + "/examples/webhookdemo/cells/hooks":                     true,
}

// cellgenSanctionedPkgs is the subset of probeNameSanctionedPkgs that requires
// the cellgen marker — any ProbeName const in these packages must live in a
// file with the cellgenMarkerLine header.
var cellgenSanctionedPkgs = map[string]bool{
	PlatformCellsModulePath + "/configcore":                                      true,
	PlatformCellsModulePath + "/auditcore":                                       true,
	PlatformCellsModulePath + "/accesscore":                                      true,
	PlatformCellsModulePath + "/registrycore":                                    true, // #2237 (303-US6)
	PlatformCellsModulePath + "/syscore":                                         true, // #1860
	PlatformModulePath + "/examples/demo/cells/democell":                         true,
	PlatformModulePath + "/examples/iotdevice/cells/devicecell":                  true,
	PlatformModulePath + "/examples/orderfulfillment/cells/orderfulfillmentcell": true,
	PlatformModulePath + "/examples/todoorder/cells/ordercell":                   true,
	PlatformModulePath + "/examples/webhookdemo/cells/hooks":                     true,
}

// adapterSanctionedPkgs requires that all ProbeName values in these packages
// end with the "_ready" suffix (adapter dependency-availability convention).
var adapterSanctionedPkgs = map[string]bool{
	PlatformModulePath + "/adapters/grpc":              true,
	PlatformModulePath + "/adapters/postgres":          true,
	PlatformModulePath + "/adapters/redis":             true,
	PlatformModulePath + "/adapters/rabbitmq":          true,
	PlatformModulePath + "/adapters/s3":                true,
	PlatformModulePath + "/adapters/vault":             true,
	PlatformModulePath + "/adapters/mqtt":              true,
	PlatformModulePath + "/adapters/oidc":              true,
	PlatformFrameworkModulePath + "/runtime/websocket": true,
	PlatformFrameworkModulePath + "/runtime/saga":      true,
}

// probenameInRepoLayerPrefixes is the set of per-layer package path prefixes
// that constitute the in-repo production scan scope for
// PROBENAME-SEALED-FUNNEL-01.  A package whose path does not start with any
// of these prefixes is skipped by the main production scanner.
var probenameInRepoLayerPrefixes = []string{
	PlatformFrameworkModulePath + "/kernel/",
	PlatformCellsModulePath + "/",
	PlatformModulePath + "/adapters/",
	PlatformFrameworkModulePath + "/runtime/",
	PlatformModulePath + "/cmd/",
	PlatformModulePath + "/examples/",
}

// probenameIsInRepoPkg reports whether pkgPath belongs to one of the
// in-repo production layers enumerated in probenameInRepoLayerPrefixes.
func probenameIsInRepoPkg(pkgPath string) bool {
	for _, prefix := range probenameInRepoLayerPrefixes {
		if strings.HasPrefix(pkgPath, prefix) {
			return true
		}
	}
	return false
}
