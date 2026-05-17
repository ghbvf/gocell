//go:build archtest_fixture

// Sibling file isolates the dot-import shape because Go does not permit
// `import . "X"` and `import "X"` in the same file. See redfixture.go for
// the qualified + alias shapes and the rule overview.
package sessionprotocolfixture

import . "github.com/ghbvf/gocell/runtime/auth/session"

func dotImportCalls() {
	_, _ = NewProtocol()
}
