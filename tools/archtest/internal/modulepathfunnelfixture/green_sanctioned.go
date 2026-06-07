//go:build archtest_fixture

package modulepathfunnelfixture

import "github.com/ghbvf/gocell/tools/archtest"

// greenSanctioned derives a child path from the REAL PlatformModulePath const
// (object identity resolved via go/types info.Uses, alias-proof). Even though it
// folds to a platform-prefixed value, the detector MUST NOT flag it: the
// sanctioned-operand exemption recognizes archtest.PlatformModulePath as the one
// permitted source. This is the form that a "naive EvaluateConstString" would
// have false-flagged — the object-identity exemption is exactly what makes the
// typed upgrade safe.
var _ = archtest.PlatformModulePath + "/pkg/z"
