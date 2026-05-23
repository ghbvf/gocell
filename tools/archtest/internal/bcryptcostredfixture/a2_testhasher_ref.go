//go:build ignore

// This file is deliberately excluded from the build (//go:build ignore): it
// references cells/accesscore/internal/credential, which tools/archtest may not
// import under Go's internal-package rule, so it could never compile. It exists
// only to be parsed (not compiled) by TestBCRYPT_COST_FUNNEL_01_A2_RedFixture,
// which asserts the A2 detector fires on a credential.NewTestHasher call in a
// non-_test.go file. parser.ParseFile ignores build constraints, so the
// detector sees this source; the Go toolchain does not.
package bcryptcostredfixture

import "github.com/ghbvf/gocell/cells/accesscore/internal/credential"

// CallTestHasherOutsideTest is the forbidden A2 shape: credential.NewTestHasher
// invoked from a non-test, non-allowlisted file.
var CallTestHasherOutsideTest = credential.NewTestHasher(4)
