package require

import (
	"os"
	"testing"
)

// PG skips the test if a PostgreSQL instance is not available. Set
// GOCELL_E2E_PG_AVAILABLE=1 in CI when a real Postgres is reachable.
func PG(t *testing.T) {
	t.Helper()
	if os.Getenv("GOCELL_E2E_PG_AVAILABLE") != "1" {
		t.Skip("postgres not available; set GOCELL_E2E_PG_AVAILABLE=1 to enable")
	}
}
