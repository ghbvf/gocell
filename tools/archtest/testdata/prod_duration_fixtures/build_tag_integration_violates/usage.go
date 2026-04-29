//go:build integration

// Package build_tag_integration_violates verifies that a file gated with
// //go:build integration is included in the scan: 1 violation expected.
package build_tag_integration_violates

import "time"

func waitForDB() {
	time.Sleep(5 * time.Second)
}
