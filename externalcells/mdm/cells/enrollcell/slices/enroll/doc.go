// Package enroll is the MDM enrollment slice of enrollcell. PR-0 (#2304) ships it
// as a skeleton: no enrollment logic yet — the device-identity / certificate /
// enrollment-saga implementation lands in MDM-PR1+ (epic #2299). It carries the
// slice's identity so the verify.unit.enroll.skeleton target resolves to a real,
// executable test (skeleton_test.go), rather than a non-runnable placeholder.
package enroll

const (
	// SliceID is the single source for this slice's metadata ID. skeleton_test.go
	// asserts it stays in sync with slice.yaml (hand-maintained until codegen
	// derives it in MDM-PR1+).
	SliceID = "enroll"

	// BelongsToCell names the owning cell, mirroring slice.yaml belongsToCell.
	BelongsToCell = "enrollcell"
)
