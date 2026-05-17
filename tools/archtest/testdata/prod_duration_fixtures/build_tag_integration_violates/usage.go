//go:build integration

// Package build_tag_integration_violates verifies that a file gated with
// //go:build integration is included in the scan:
// 1 violation expected (declared via spec.Violation()).
package build_tag_integration_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func waitForDB() {
	spec.Violation()
	time.Sleep(5 * time.Second)
}
