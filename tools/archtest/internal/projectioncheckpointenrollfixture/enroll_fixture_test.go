//go:build archtest_fixture

package projectioncheckpointenrollfixture

import (
	"testing"

	"github.com/ghbvf/gocell/kernel/projection/projectiontest"
)

// TestEnrolledStoreConformance enrolls enrolledStore in the shared conformance
// harness. The enroll archtest's RED fixture scans this call to credit
// enrolledStore (positive direction). unenrolledStore is intentionally absent
// from any RunCheckpointConformance call.
//
// Under the archtest_fixture build tag this file is type-checked by
// Run(t, archtest.Fixture(...)) but never executed by normal `go test` (the tag is not
// set in normal builds), so it exists purely as a real, resolvable enrollment
// callsite for the scanner — not as a running conformance suite.
func TestEnrolledStoreConformance(t *testing.T) {
	projectiontest.RunCheckpointConformance(t, newEnrolledStore())
}
