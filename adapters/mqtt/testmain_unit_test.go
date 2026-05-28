//go:build !integration

package mqtt

import (
	"os"
	"testing"
)

// TestMain (unit build) runs the package tests and then stops the shared
// in-process mochi broker that initSharedInternalBroker starts lazily, so the
// broker goroutine and its listener socket are released rather than leaked to
// process exit.
//
// The integration build supplies its own TestMain (testmain_integration_test.go,
// //go:build integration) for the Mosquitto container; the mutually-exclusive
// build tags (!integration here, integration there) guarantee exactly one
// TestMain per build configuration — Go forbids two.
func TestMain(m *testing.M) {
	code := m.Run()
	stopSharedInternalBroker()
	os.Exit(code)
}
