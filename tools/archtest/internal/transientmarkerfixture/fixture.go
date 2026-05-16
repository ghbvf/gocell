//go:build archtest_fixture

// Package transientmarkerfixture is a deliberate
// ADAPTER-ERROR-CLASSIFICATION-TRANSIENT-01 reverse self-check corpus. It
// models pkg/errcode's Error.transient marker funnel: only func WrapInfra is
// allowed to write the unexported transient field. The fixture contains:
//
//   - WrapInfra      — the allowed writer (clean; MUST NOT be reported)
//   - badAssign      — RED: assignment write outside WrapInfra
//   - badLiteral     — RED: composite-literal write outside WrapInfra
//   - readOnly       — clean: a read of transient (MUST NOT be reported)
//
// The detector scanTransientMarkerWrites, pointed at this package's import
// path, must report exactly the two RED sites. Loaded as a real Go package
// via packages.Load with the archtest_fixture build tag — bypassing the
// reverse self-check requires editing this real source.
package transientmarkerfixture

// Error mirrors errcode.Error's marker-bearing shape.
type Error struct {
	Code      string
	transient bool
}

// WrapInfra is the single allowed writer of the transient marker.
func WrapInfra(code string) *Error {
	e := &Error{Code: code}
	e.transient = true // allowed: enclosing func is WrapInfra
	return e
}

// badAssign writes the marker via assignment outside WrapInfra (RED).
func badAssign(code string) *Error {
	e := &Error{Code: code}
	e.transient = true // RED: assignment outside WrapInfra
	return e
}

// badLiteral writes the marker via composite literal outside WrapInfra (RED).
func badLiteral(code string) *Error {
	return &Error{Code: code, transient: true} // RED: composite literal outside WrapInfra
}

// readOnly only reads the marker — must not be flagged as a write.
func readOnly(e *Error) bool {
	if e.transient {
		return true
	}
	return false
}

var _ = readOnly
var _ = badAssign
var _ = badLiteral
