//go:build archtest_fixture

// Sibling file isolates the dot-import shape because Go does not permit
// `import . "X"` and `import "X"` in the same file. See redfixture.go for the
// qualified + aliased shapes and the rule overview.
package capfunnelfixture

import . "github.com/ghbvf/gocell/adapters/postgres"

// dotImportCall exercises dot-import resolution (1 hit).
func dotImportCall() {
	var p *Pool
	_ = NewTxManager(p)
}
