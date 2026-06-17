package governance

// GovernanceGateResult is the K8s AdmissionResponse-style outcome of a
// RegistrationGate decision (303-US3, #2234), modeled on
// admission/v1.AdmissionResponse{Allowed, Result, Warnings}:
//
//   - Allowed  : the verdict. false = denied (the contract must not be persisted).
//   - Result   : the blocking error findings explaining a denial (the deny detail,
//     ~ AdmissionResponse.Result *Status). Empty when Allowed.
//   - Warnings : non-blocking advisory findings. May be non-empty even when Allowed
//     (~ AdmissionResponse.Warnings).
//   - Reason   : the sealed, machine-readable reason for the verdict, so a caller
//     (e.g. a registry HTTP handler) can branch without parsing English text.
//
// ref: kubernetes/api admission/v1/types.go — AdmissionResponse.
type GovernanceGateResult struct {
	Allowed  bool
	Result   []ValidationResult
	Warnings []ValidationResult
	Reason   GateReason
}

// GateReason is the sealed, machine-readable reason a gate verdict took. A
// non-zero GateReason cannot be constructed outside this package — the single
// field is unexported and the only values are the package-private singletons
// exposed via the accessor functions below (reassigning a function is a compile
// error). This makes "an external package cannot forge a reason" expressible at
// compile time (AI-robust Hard), mirroring registry.RegistrationState and
// transport.TransportOutcome.
//
// The zero value is invalid: String() renders it as [GateReasonUnknown]
// (fail-closed), never the empty string, and it is not a member of allGateReasons.
type GateReason struct {
	// v is the wire/label value ("allowed" | "validation-failed" | …). Unexported:
	// a non-zero GateReason cannot be constructed outside this package.
	v string
}

// GateReasonUnknown is the fail-closed render of a zero-value (forged) GateReason.
// It is NOT a producible reason (not in allGateReasons). To test for a forged
// reason use IsZero(), not a string comparison.
const GateReasonUnknown = "unknown"

// String returns the wire/label value. The zero value renders as
// [GateReasonUnknown] (fail-closed), never the empty string.
func (r GateReason) String() string {
	if r.v == "" {
		return GateReasonUnknown
	}
	return r.v
}

// IsZero reports whether r is the zero (forged/uninitialised) value.
func (r GateReason) IsZero() bool { return r.v == "" }

// Package-private singletons — the sole GateReason values. Exposed via accessor
// functions (not exported vars) so the registered values are immutable.
var (
	reasonAllowed              = GateReason{v: "allowed"}
	reasonValidationFailed     = GateReason{v: "validation-failed"}
	reasonValidatorUnavailable = GateReason{v: "validator-unavailable"}
	reasonTenantInvalid        = GateReason{v: "tenant-invalid"}
	reasonDuplicate            = GateReason{v: "duplicate"}
)

// ReasonAllowed: the candidate passed every applicable governance rule.
func ReasonAllowed() GateReason { return reasonAllowed }

// ReasonValidationFailed: the candidate violated a governance rule (including a
// REG-01 fanout-completeness error), or a malformed/missing candidate.
func ReasonValidationFailed() GateReason { return reasonValidationFailed }

// ReasonValidatorUnavailable: the validation run could not complete (ctx
// canceled / interrupted, or a recovered rule panic) — fail-closed, never
// fail-open.
func ReasonValidatorUnavailable() GateReason { return reasonValidatorUnavailable }

// ReasonTenantInvalid: the request carried an empty / non-canonical tenant
// (FR-002 "缺租户 → deny").
func ReasonTenantInvalid() GateReason { return reasonTenantInvalid }

// ReasonDuplicate: the contract was valid but a registration with the same id
// already exists (a conflict surfaced by the store at Submit).
func ReasonDuplicate() GateReason { return reasonDuplicate }

// allGateReasons is the closed registry of every GateReason value. It backs the
// anti-vacuity freeze (TestGateReason_FrozenRegistry): a new reason added without
// registering it here — or a renamed wire value — is caught.
var allGateReasons = []GateReason{
	reasonAllowed, reasonValidationFailed, reasonValidatorUnavailable,
	reasonTenantInvalid, reasonDuplicate,
}

// isRegistered reports whether r is one of the producible reasons. A zero value
// (forged) is NOT registered.
func (r GateReason) isRegistered() bool {
	for _, x := range allGateReasons {
		if x == r {
			return true
		}
	}
	return false
}
