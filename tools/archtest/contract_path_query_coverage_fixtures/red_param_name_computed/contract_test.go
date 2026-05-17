//go:build archtest_fixture

// Package redparamnamecomputed_test is a RED fixture for
// CONTRACT-PATH-QUERY-PARAM-NAME-LITERAL-01. It calls MustRejectQueryParam
// with a runtime variable as the param name (call.Args[1]). The rule MUST
// flag this so a future regression cannot silently let a runtime param-name
// expression masquerade as covered.
package redparamnamecomputed_test

import (
	"fmt"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/ghbvf/gocell/tests/contracttest"
)

func fixtureContractsRoot() (string, error) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		return "", fmt.Errorf("fixture: runtime.Caller failed")
	}
	return filepath.Join(filepath.Dir(thisFile), "contracts"), nil
}

// TestFixtureParamNameComputed calls MustRejectQueryParam with a runtime
// variable as the param-name argument. PARAM-NAME-LITERAL-01 MUST flag it.
func TestFixtureParamNameComputed(t *testing.T) {
	root, err := fixtureContractsRoot()
	if err != nil {
		t.Fatalf("fixture root: %v", err)
	}
	c := contracttest.LoadByID(t, root, "http.test.paramnamecomputed.v1")
	paramName := "limit" // runtime variable, not a const
	c.MustRejectQueryParam(t, paramName, "0")
}
