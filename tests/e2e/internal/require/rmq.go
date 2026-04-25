package require

import (
	"os"
	"testing"
)

// RMQ skips the test if a RabbitMQ broker is not available. Set
// GOCELL_E2E_RMQ_AVAILABLE=1 in CI when a real RabbitMQ is reachable.
func RMQ(t *testing.T) {
	t.Helper()
	if os.Getenv("GOCELL_E2E_RMQ_AVAILABLE") != "1" {
		t.Skip("rabbitmq not available; set GOCELL_E2E_RMQ_AVAILABLE=1 to enable")
	}
}
