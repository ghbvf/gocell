package archtest

// health_verbose_invariants.go — platform symbol path constants for
// HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01 and HEALTH-REDACTED-ERROR-MSG-FUNNEL-01.
//
// All paths are anchored to PlatformModulePath so a module rename updates
// exactly one place (external.go). Scanner logic lives in the _test.go file.

const (
	// healthPackageImportPath is the import path of the runtime/http/health
	// package whose verboseDependencyEntry wire shape and redactedErrorMsg funnel
	// are locked by HEALTH-VERBOSE-WIRE-SHAPE-FROZEN-01 and
	// HEALTH-REDACTED-ERROR-MSG-FUNNEL-01.
	healthPackageImportPath = PlatformFrameworkModulePath + "/runtime/http/health"

	// redactionPkgPath is the import path of the pkg/redaction package whose
	// RedactString function the newRedactedErrorMsg funnel must call.
	redactionPkgPath = PlatformFrameworkModulePath + "/pkg/redaction"
)
