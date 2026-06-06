package archtest

// observability_metrics.go — importable METRICS-GAUGEVEC-FUNNEL-01 /
// METRICS-GAUGEVEC-UPSTREAM-HARD-01 / METRICS-ADAPTERPROM-CALLER-ALLOWLIST-01
// rule constants.
//
// Platform package paths are anchored to PlatformModulePath so a module rename
// updates exactly one place. The scanner logic lives in the _test.go file; only
// the path constants are placed here for potential importability.
//
// This file is NOT registered in StandardCellRules().

const (
	// adapterPromPkg is the import path of the public adapters/prometheus package
	// whose New*/Register* constructors are locked by
	// METRICS-ADAPTERPROM-CALLER-ALLOWLIST-01.
	adapterPromPkg = PlatformModulePath + "/adapters/prometheus"

	// promwrapPkg is the exact import path for the internal promwrap package that
	// is permitted to call the banned Prometheus constructors directly. Exact
	// equality (not strings.Contains) prevents sibling packages from being
	// accidentally allowed.
	promwrapPkg = PlatformModulePath + "/adapters/prometheus/internal/promwrap"

	// otelwrapPkg is the exact import path for the internal otelwrap package that
	// is permitted to call the banned OTel metric constructors directly. Exact
	// equality (not strings.Contains) prevents sibling packages from being
	// accidentally allowed.
	otelwrapPkg = PlatformModulePath + "/adapters/otel/internal/otelwrap"
)
