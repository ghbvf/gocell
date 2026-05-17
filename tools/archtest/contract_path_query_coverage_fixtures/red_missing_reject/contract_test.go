//go:build archtest_fixture

package redmissingreject_test

import (
	"testing"

	"github.com/ghbvf/gocell/tests/contracttest"
)

// This fixture deliberately omits any MustRejectPathParam call so the
// CONTRACT-PATH-QUERY-COVERAGE-01 archtest detects the missing coverage.
func TestFixtureMissingReject(t *testing.T) {
	root := contracttest.ContractsRoot(t)
	c := contracttest.LoadByID(t, root, "http.test.paramcoverage.v1")
	// Only calls ValidatePathParam — never MustRejectPathParam.
	// This is the RED fixture; the archtest must report it as uncovered.
	c.ValidatePathParam(t, "key", "hello")
}
