//go:build integration

package saga_test

import (
	"os"
	"testing"

	"github.com/ghbvf/gocell/adapters/postgres/pgtest"
)

// sharedPG owns one PostgreSQL container for the adapters/postgres/saga test
// binary. Migration set is the production set (all 014..040), applied once to
// the template via pgtest; per-test DBs are file-clones.
//
// pgtest is the documented sanctioned holder for adapter test binaries that
// CAN import adapters/postgres (saga_test is package-external, so it can).
var sharedPG = pgtest.New("gocell_adapters_postgres_saga_test_template")

func TestMain(m *testing.M) {
	code := m.Run()
	sharedPG.Shutdown()
	os.Exit(code)
}
