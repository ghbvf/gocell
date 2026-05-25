//go:build integration

package saga_test

import (
	"os"
	"testing"

	"github.com/ghbvf/gocell/tests/testutil/pgshare"
)

// sharedPG owns one PostgreSQL container for the adapters/postgres/saga test
// binary. Migration set is the production set (all 014..040), applied once to
// the template via pgshare; per-test DBs are file-clones.
//
// pgshare is the documented sanctioned holder for adapter test binaries that
// CAN import adapters/postgres (saga_test is package-external, so it can).
var sharedPG = pgshare.New("gocell_adapters_postgres_saga_test_template")

func TestMain(m *testing.M) {
	code := m.Run()
	sharedPG.Shutdown()
	os.Exit(code)
}
