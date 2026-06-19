// Package status is the MDM cert-status slice of enrollcell. It provides an
// in-memory certificate-status repository and the generated Service implementation
// for http.deviceidentity.status.v1 (ownerCell: _framework, lifecycle: draft).
//
// The framework-owned contract is served via the composition-root framework-serving
// harness (ADR 202606130635-1939 D4, bootstrap.WithFrameworkHTTPServing) — not via
// a cell contractUsage. The route is mounted from cmd/mdmd via Service.FrameworkRoute().
//
// PR-1 serves the status endpoint in coarse admin/operator mode (device:read).
// Device-self (query-param deviceId == subject) and PR-2's cert write path will
// extend this slice. Multiple-tenant PG FORCE RLS lands in PR-15.
package status

const (
	// SliceID is the single source for this slice's metadata ID. skeleton_test.go
	// asserts it stays in sync with slice.yaml.
	SliceID = "status"

	// BelongsToCell names the owning cell, mirroring slice.yaml belongsToCell.
	BelongsToCell = "enrollcell"
)
