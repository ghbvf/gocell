//go:build archtest_fixture

package modulepathfunnelfixture

import "github.com/ghbvf/gocell/tools/archtest"

// Go const-group INHERITED RHS (spec §Constant declarations): qInherit omits its
// expression list, so it repeats the preceding non-empty one —
// archtest.PlatformModulePath + "/a" — and equals "github.com/ghbvf/gocell/a".
// The blank-first const keeps the group unused-clean while seeding the inherited
// expression.
const (
	_        = archtest.PlatformModulePath + "/a"
	qInherit // inherits the line above
)

// The blank var folds to a platform CHILD value via qInherit, whose provenance
// (through its INHERITED RHS) reaches PlatformModulePath. It MUST NOT be flagged:
// buildPackageConstRHS must model Go's const-group inheritance, else this legal
// derived const is a FALSE POSITIVE (Codex review F1, #1749).
var _ = qInherit + "/b"
