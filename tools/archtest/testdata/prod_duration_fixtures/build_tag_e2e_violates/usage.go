//go:build e2e

// Package build_tag_e2e_violates verifies that a file gated with //go:build e2e
// is included in the scan when tags={e2e,integration,pg}: 1 violation expected.
package build_tag_e2e_violates

import "time"

func waitForService() {
	time.Sleep(5 * time.Second)
}
