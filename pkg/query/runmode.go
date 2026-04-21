package query

// RunMode controls fail-open vs fail-closed semantics in the paged-query
// helper. The zero value is RunModeProd, so a caller that forgets to set
// this field defaults to fail-closed (strict cursor validation).
//
// ref: zeromicro/go-zero core/service/serviceconf.go — explicit Mode enum
// drives behavior; defaults to the strictest value (ProMode).
//
// ref: ThreeDotsLabs/watermill — nopPublisher lives only in _test.go;
// demo vs production is decided at the injection site, not by sniffing
// data at runtime. GoCell follows the same principle: callers declare
// the mode explicitly instead of inferring it from key material.
type RunMode uint8

const (
	// RunModeProd rejects malformed or stale cursor tokens with an error.
	// This is the zero value — unset callers get strict behavior.
	RunModeProd RunMode = 0

	// RunModeDemo allows the paged-query helper to silently fall back to
	// the first page when a cursor fails to decode (e.g. after a key rotation
	// in a demo deployment with no operator). Scope/context mismatches still
	// return errors even in demo mode because they indicate a client bug.
	RunModeDemo RunMode = 1
)

// IsDemo reports whether the run mode allows demo-only fallbacks.
func (m RunMode) IsDemo() bool { return m == RunModeDemo }

// String returns a stable lowercase label suitable for structured logs.
func (m RunMode) String() string {
	switch m {
	case RunModeProd:
		return "prod"
	case RunModeDemo:
		return "demo"
	default:
		return "unknown"
	}
}

// RunModeForDemo returns RunModeDemo when demo is true, RunModeProd otherwise.
// Convenience helper for callers that already track their demo-mode decision
// as a boolean (e.g. translating kernel/cell.DurabilityMode at wire time).
//
// Do not extend: this function is the ONLY permitted translation point between
// kernel/cell.DurabilityMode (or any other "is-demo" signal) and pkg/query.RunMode.
// Call it exactly once at Cell Init() time and pass the resulting RunMode down
// to slice services and PagedQueryConfig via constructor parameters. Do NOT call
// it again inside individual slice methods, handlers, or repositories — that
// scatters demo semantics across the call graph and defeats the single wire-time
// decision. Do NOT add a new RunMode value without a corresponding change in the
// calling layer; the two enums must stay in 1-to-1 correspondence.
//
// ref: zeromicro/go-zero core/service/serviceconf.go — ServiceConf.Mode is
// resolved once at MustSetUp() and propagated by injection, not re-sniffed.
func RunModeForDemo(demo bool) RunMode {
	if demo {
		return RunModeDemo
	}
	return RunModeProd
}

// PublishFailureMode controls whether direct publisher failures on write paths
// are propagated (fail-closed) or tolerated (fail-open) when no outbox writer
// is configured.
//
// Placed in pkg/query alongside RunMode because both types serve the same role:
// translating a single DurabilityMode signal (prod vs demo) into concrete
// fail-closed vs fail-open behaviors at Cell init time. Collocating them
// keeps the demo-mode translation logic together and prevents pkg/query from
// importing kernel/cell (preserving the kernel-agnostic layering of shared
// utilities). This is a pragmatic choice within the current PR scope; a future
// refactor may extract both types into a new pkg/runmode if additional
// fail-open modes emerge.
type PublishFailureMode uint8

const (
	// PublishFailureModeFailClosed propagates publisher failures.
	// This is the zero value and therefore the safe default.
	PublishFailureModeFailClosed PublishFailureMode = 0

	// PublishFailureModeFailOpen swallows publisher failures after warning logs.
	PublishFailureModeFailOpen PublishFailureMode = 1
)

// IsFailOpen reports whether publisher failures are tolerated.
func (m PublishFailureMode) IsFailOpen() bool { return m == PublishFailureModeFailOpen }

// String returns a stable lowercase label suitable for structured logs.
func (m PublishFailureMode) String() string {
	switch m {
	case PublishFailureModeFailClosed:
		return "fail-closed"
	case PublishFailureModeFailOpen:
		return "fail-open"
	default:
		return "unknown"
	}
}

// PublishFailureModeForDemo maps demo=true to fail-open and all other modes to
// fail-closed at wire time.
func PublishFailureModeForDemo(demo bool) PublishFailureMode {
	if demo {
		return PublishFailureModeFailOpen
	}
	return PublishFailureModeFailClosed
}
