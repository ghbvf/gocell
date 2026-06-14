package archtest

// metrics_cancel_ctx_conformance.go — platform symbol path constants for
// METRICS-CANCEL-CTX-CONFORMANCE-01.
//
// All paths are anchored to PlatformModulePath so a module rename updates
// exactly one place (external.go). Scanner logic lives in the _test.go file.

const (
	// metricsProviderIfacePkg is the import path of the metrics.Provider
	// interface package.
	metricsProviderIfacePkg = PlatformFrameworkModulePath + "/kernel/observability/metrics"

	// cancelCtxConformancePkg is the import path of the
	// metricstest.RunCanceledCtxConformance conformance helper.
	cancelCtxConformancePkg = PlatformFrameworkModulePath + "/kernel/observability/metrics/metricstest"

	// metricsConformanceOtelPkg is the import path of the otel metrics adapter
	// package (used in the RED fixture).
	metricsConformanceOtelPkg = PlatformModulePath + "/adapters/otel"

	// metricsConformancePromPkg is the import path of the prometheus metrics
	// adapter package (used in the RED fixture).
	metricsConformancePromPkg = PlatformModulePath + "/adapters/prometheus"
)
