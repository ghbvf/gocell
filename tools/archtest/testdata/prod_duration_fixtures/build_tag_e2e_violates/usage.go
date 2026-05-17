//go:build e2e

// Package build_tag_e2e_violates verifies that a file gated with //go:build e2e
// is included in the scan when tags={e2e,integration,pg}:
// 1 violation expected (declared via spec.Violation()).
package build_tag_e2e_violates

import (
	"time"

	spec "github.com/ghbvf/gocell/tools/archtest/fixturespec"
)

func waitForService() {
	spec.Violation()
	time.Sleep(5 * time.Second)
}
