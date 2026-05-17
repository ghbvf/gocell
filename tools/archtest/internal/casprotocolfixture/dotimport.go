//go:build archtest_fixture

// Sibling file isolates the dot-import shape because Go does not permit
// `import . "X"` and `import "X"` in the same file. See redfixture.go for
// the qualified + aliased shapes and the rule overview.
package casprotocolfixture

import . "github.com/ghbvf/gocell/runtime/state/cas"

func dotImportCall() {
	_, _ = NewProtocol(
		WithVersionField("version"),
	)
}
