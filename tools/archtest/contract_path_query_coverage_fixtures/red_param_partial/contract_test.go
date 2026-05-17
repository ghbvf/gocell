//go:build archtest_fixture

// Package redparampartial_test is a RED fixture for
// CONTRACT-PATH-QUERY-COVERAGE-01 per-param granularity. The contract declares
// two query params (limit, cursor); this fixture only calls
// MustRejectQueryParam for limit. The rule MUST flag cursor as uncovered AND
// MUST NOT flag limit as uncovered.
package redparampartial_test

import (
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ghbvf/gocell/tests/contracttest"
)

// fixtureContractsRoot returns the absolute path of the contracts dir for
// this fixture package. Computed via runtime.Caller so the test does not
// depend on module-root contracts/.
func fixtureContractsRoot() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("fixture: runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "contracts"), nil
}

// TestFixtureParamPartial exercises only the `limit` query param. The contract
// also declares `cursor`, which is left uncovered on purpose.
func TestFixtureParamPartial(t *testing.T) {
	root, err := fixtureContractsRoot()
	if err != nil {
		t.Fatalf("fixture root: %v", err)
	}
	c := contracttest.LoadByID(t, root, "http.test.paramcoverage-partial.v1")
	c.MustRejectQueryParam(t, "limit", "0")
}
